package viamchess

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"os"
	"strings"
	"sync"
	"time"

	"go.uber.org/multierr"

	"github.com/golang/geo/r3"

	"github.com/mitchellh/mapstructure"

	"go.viam.com/rdk/components/arm"
	"go.viam.com/rdk/components/gripper"
	"go.viam.com/rdk/components/switch"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/referenceframe"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/robot/framesystem"
	generic "go.viam.com/rdk/services/generic"
	"go.viam.com/rdk/services/motion"
	"go.viam.com/rdk/services/vision"
	"go.viam.com/rdk/spatialmath"
	viz "go.viam.com/rdk/vision"
	"go.viam.com/rdk/vision/objectdetection"
	"go.viam.com/rdk/vision/viscapture"

	"github.com/corentings/chess/v2"
	"github.com/corentings/chess/v2/uci"

	"github.com/erh/vmodutils/touch"
)

var ChessModel = family.WithModel("chess")

const safeZ = 200.0

func init() {
	resource.RegisterService(generic.API, ChessModel,
		resource.Registration[resource.Resource, *ChessConfig]{
			Constructor: newViamChessChess,
		},
	)
}

type ChessConfig struct {
	PieceFinder string `json:"piece-finder"`

	Arm     string
	Gripper string

	PoseStart string `json:"pose-start"`

	Engine       string
	EngineMillis int `json:"engine-millis"`
}

func (cfg *ChessConfig) engine() string {
	if cfg.Engine == "" {
		return "stockfish"
	}
	return cfg.Engine
}

func (cfg *ChessConfig) engineMillis() int {
	if cfg.EngineMillis <= 0 {
		return 10
	}
	return cfg.EngineMillis
}

func (cfg *ChessConfig) Validate(path string) ([]string, []string, error) {
	if cfg.PieceFinder == "" {
		return nil, nil, fmt.Errorf("need a piece-finder")
	}
	if cfg.Arm == "" {
		return nil, nil, fmt.Errorf("need an arm")
	}
	if cfg.Gripper == "" {
		return nil, nil, fmt.Errorf("need a gripper")
	}
	if cfg.PoseStart == "" {
		return nil, nil, fmt.Errorf("need a pose-start")
	}

	return []string{cfg.PieceFinder, cfg.Arm, cfg.Gripper, cfg.PoseStart, motion.Named("builtin").String()}, nil, nil
}

type viamChessChess struct {
	resource.AlwaysRebuild

	name resource.Name

	logger logging.Logger
	conf   *ChessConfig

	cancelCtx  context.Context
	cancelFunc func()

	pieceFinder vision.Service
	arm         arm.Arm
	gripper     gripper.Gripper

	poseStart toggleswitch.Switch

	motion motion.Service
	rfs    framesystem.Service

	startPose *referenceframe.PoseInFrame

	engine *uci.Engine

	fenFile string

	doCommandLock sync.Mutex
}

func newViamChessChess(ctx context.Context, deps resource.Dependencies, rawConf resource.Config, logger logging.Logger) (resource.Resource, error) {
	conf, err := resource.NativeConfig[*ChessConfig](rawConf)
	if err != nil {
		return nil, err
	}

	return NewChess(ctx, deps, rawConf.ResourceName(), conf, logger)

}

func NewChess(ctx context.Context, deps resource.Dependencies, name resource.Name, conf *ChessConfig, logger logging.Logger) (resource.Resource, error) {
	var err error

	cancelCtx, cancelFunc := context.WithCancel(context.Background())

	s := &viamChessChess{
		name:       name,
		logger:     logger,
		conf:       conf,
		cancelCtx:  cancelCtx,
		cancelFunc: cancelFunc,
	}

	s.pieceFinder, err = vision.FromProvider(deps, conf.PieceFinder)
	if err != nil {
		return nil, err
	}

	s.arm, err = arm.FromProvider(deps, conf.Arm)
	if err != nil {
		return nil, err
	}

	s.gripper, err = gripper.FromProvider(deps, conf.Gripper)
	if err != nil {
		return nil, err
	}

	s.poseStart, err = toggleswitch.FromProvider(deps, conf.PoseStart)
	if err != nil {
		return nil, err
	}

	s.motion, err = motion.FromDependencies(deps, "builtin")
	if err != nil {
		return nil, err
	}

	s.rfs, err = framesystem.FromDependencies(deps)
	if err != nil {
		logger.Errorf("can't find framesystem: %v", err)
	}

	err = s.goToStart(ctx)
	if err != nil {
		return nil, err
	}

	s.fenFile = os.Getenv("VIAM_MODULE_DATA") + "state.json"
	s.logger.Infof("fenFile: %v", s.fenFile)
	s.engine, err = uci.New(conf.engine())
	if err != nil {
		return nil, err
	}

	err = s.engine.Run(uci.CmdUCI, uci.CmdIsReady, uci.CmdUCINewGame) // TODO: not sure this is correct
	if err != nil {
		return nil, err
	}

	return s, nil
}

func (s *viamChessChess) Name() resource.Name {
	return s.name
}

// ----

type MoveCmd struct {
	From, To string
	N        int
}

type cmdStruct struct {
	Move   MoveCmd
	Go     int
	Reset  bool
	Wipe   bool
	Center bool
}

func (s *viamChessChess) DoCommand(ctx context.Context, cmdMap map[string]interface{}) (map[string]interface{}, error) {
	s.doCommandLock.Lock()
	defer s.doCommandLock.Unlock()

	defer func() {
		err := s.goToStart(ctx)
		if err != nil {
			s.logger.Warnf("can't go home: %v", err)
		}
	}()
	var cmd cmdStruct
	err := mapstructure.Decode(cmdMap, &cmd)
	if err != nil {
		return nil, err
	}

	if cmd.Move.To != "" && cmd.Move.From != "" {
		s.logger.Infof("move %v to %v", cmd.Move.From, cmd.Move.To)

		for x := range cmd.Move.N {
			err := s.goToStart(ctx)
			if err != nil {
				return nil, err
			}

			from, to := cmd.Move.From, cmd.Move.To
			if x%2 == 1 {
				to, from = from, to
			}
			all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
			if err != nil {
				return nil, err
			}

			err = s.movePiece(ctx, all, nil, from, to, nil)
			if err != nil {
				return nil, err
			}
		}

		return nil, nil
	}

	if cmd.Go > 0 {
		err := s.checkPositionForMoves(ctx)
		if err != nil {
			return nil, err
		}

		var m *chess.Move
		for range cmd.Go {
			m, err = s.makeAMove(ctx)
			if err != nil {
				return nil, err
			}
		}
		return map[string]interface{}{"move": m.String()}, nil
	}

	if cmd.Reset {
		return nil, s.resetBoard(ctx)
	}

	if cmd.Wipe {
		return nil, s.wipe(ctx)
	}

	if cmd.Center {
		return nil, s.centerCamera(ctx)
	}

	return nil, fmt.Errorf("bad cmd %v", cmdMap)
}

func (s *viamChessChess) Close(context.Context) error {
	var err error

	s.cancelFunc()

	if s.engine != nil {
		err = multierr.Combine(err, s.engine.Close())
	}

	return err
}

func (s *viamChessChess) findObject(data viscapture.VisCapture, pos string) *viz.Object {
	for _, o := range data.Objects {
		if strings.HasPrefix(o.Geometry.Label(), pos) {
			return o
		}
	}
	return nil
}

func (s *viamChessChess) findDetection(data viscapture.VisCapture, pos string) objectdetection.Detection {
	for _, d := range data.Detections {
		if strings.HasPrefix(d.Label(), pos) {
			return d
		}
	}
	return nil
}

func (s *viamChessChess) graveyardPosition(data viscapture.VisCapture, pos int) (r3.Vector, error) {
	f := 8 - (pos % 8)
	ex := 1 + (pos / 8)

	k := fmt.Sprintf("a%d", f)
	oo := s.findObject(data, k)
	if oo == nil {
		return r3.Vector{}, fmt.Errorf("why no object for %s", k)
	}

	md := oo.MetaData()
	return r3.Vector{md.Center().X, md.Center().Y - float64(ex*80), 60}, nil

}

func (s *viamChessChess) getCenterFor(data viscapture.VisCapture, pos string, theState *state) (r3.Vector, error) {
	if pos == "-" {
		if s == nil {
			return r3.Vector{400, -400, 200}, nil
		}
		return s.graveyardPosition(data, len(theState.graveyard))
	}

	if pos[0] == 'X' {
		x := -1
		_, err := fmt.Sscanf(pos, "X%d", &x)
		if err != nil {
			return r3.Vector{}, fmt.Errorf("bad special graveyard (%s)", pos)
		}

		return s.graveyardPosition(data, x)
	}

	o := s.findObject(data, pos)
	if o == nil {
		return r3.Vector{}, fmt.Errorf("can't find object for: %s", pos)
	}

	md := o.MetaData()
	center := md.Center()

	if strings.HasSuffix(o.Geometry.Label(), "-0") {
		return center, nil
	}

	high := touch.PCFindHighestInRegion(o, image.Rect(-1000, -1000, 1000, 1000))
	return r3.Vector{
		X: (center.X + high.X) / 2,
		Y: (center.Y + high.Y) / 2,
		Z: high.Z,
	}, nil
}

func (s *viamChessChess) movePiece(ctx context.Context, data viscapture.VisCapture, theState *state, from, to string, m *chess.Move) error {
	s.logger.Infof("movePiece called: %s -> %s", from, to)
	if to != "-" && to[0] != 'X' { // check where we're going
		o := s.findObject(data, to)
		if o == nil {
			return fmt.Errorf("can't find object for: %s", to)
		}

		if !strings.HasSuffix(o.Geometry.Label(), "-0") {

			what := "?"

			s.logger.Infof("position %s already has a piece (%s) (%s), will move", to, what, o.Geometry.Label())
			err := s.movePiece(ctx, data, theState, to, "-", nil)
			if err != nil {
				return fmt.Errorf("can't move piece out of the way: %w", err)
			}

			if theState != nil {
				pc := theState.game.Position().Board().Piece(m.S2())
				theState.graveyard = append(theState.graveyard, int(pc))
			}

		}
	}

	useZ := 100.0

	{
		center, err := s.getCenterFor(data, from, theState)
		if err != nil {
			return err
		}
		useZ = center.Z

		err = s.setupGripper(ctx)
		if err != nil {
			return err
		}

		err = s.moveGripper(ctx, r3.Vector{center.X, center.Y, safeZ})
		if err != nil {
			return err
		}

		for {
			err = s.moveGripper(ctx, r3.Vector{center.X, center.Y, useZ})
			if err != nil {
				return err
			}

			got, err := s.myGrab(ctx)
			if err != nil {
				return err
			}
			if got {
				break
			}

			useZ -= 10
			if useZ < 12 { // todo: magic number
				return fmt.Errorf("couldn't grab, and scared to go lower")
			}

			s.logger.Warnf("didn't grab, going to try a little more")

			err = s.setupGripper(ctx)
			if err != nil {
				return err
			}
			time.Sleep(250 * time.Millisecond)
		}

		err = s.moveGripper(ctx, r3.Vector{center.X, center.Y, safeZ})
		if err != nil {
			return err
		}
	}

	{
		center, err := s.getCenterFor(data, to, theState)
		if err != nil {
			return err
		}

		err = s.moveGripper(ctx, r3.Vector{center.X, center.Y, safeZ})
		if err != nil {
			return err
		}

		err = s.moveGripper(ctx, r3.Vector{center.X, center.Y, useZ})
		if err != nil {
			return err
		}

		err = s.setupGripper(ctx)
		if err != nil {
			return err
		}

		err = s.moveGripper(ctx, r3.Vector{center.X, center.Y, safeZ})
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *viamChessChess) goToStart(ctx context.Context) error {
	err := s.poseStart.SetPosition(ctx, 2, nil)
	if err != nil {
		return err
	}
	err = s.gripper.Open(ctx, nil)
	if err != nil {
		return err
	}

	time.Sleep(time.Second)

	s.startPose, err = s.rfs.GetPose(ctx, s.conf.Gripper, "world", nil, nil)
	if err != nil {
		return err
	}

	return nil
}

func (s *viamChessChess) setupGripper(ctx context.Context) error {
	_, err := s.arm.DoCommand(ctx, map[string]interface{}{"move_gripper": 450.0})
	return err
}

func (s *viamChessChess) moveGripper(ctx context.Context, p r3.Vector) error {

	orientation := &spatialmath.OrientationVectorDegrees{
		OZ:    -1,
		Theta: s.startPose.Pose().Orientation().OrientationVectorDegrees().Theta,
	}

	if p.X > 300 {
		orientation.OX = (p.X - 300) / 1000
	}

	if p.Y < -300 {
		orientation.OY = (p.Y + 300) / 300
		orientation.OX += .2
	}

	myPose := spatialmath.NewPose(p, orientation)
	_, err := s.motion.Move(ctx, motion.MoveReq{
		ComponentName: s.conf.Gripper,
		Destination:   referenceframe.NewPoseInFrame("world", myPose),
	})
	if err != nil {
		return fmt.Errorf("can't move to %v: %w", myPose, err)
	}
	return nil
}

type state struct {
	game      *chess.Game
	graveyard []int
}

type savedState struct {
	FEN       string `json:"fen"`
	Graveyard []int  `json:"graveyard"`
}

func (s *viamChessChess) getGame(ctx context.Context) (*state, error) {
	return readState(ctx, s.fenFile)
}

func readState(ctx context.Context, fn string) (*state, error) {
	data, err := os.ReadFile(fn)
	if os.IsNotExist(err) {
		return &state{chess.NewGame(), []int{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("error reading fen (%s) %T", fn, err)
	}

	ss := savedState{}
	err = json.Unmarshal(data, &ss)
	if err != nil {
		return nil, fmt.Errorf("cannot unmarshal json")
	}

	f, err := chess.FEN(ss.FEN)
	if err != nil {
		return nil, fmt.Errorf("invalid fen from (%s) (%s) %w", fn, data, err)
	}
	return &state{chess.NewGame(f), ss.Graveyard}, nil
}

func (s *viamChessChess) saveGame(ctx context.Context, theState *state) error {
	ss := savedState{
		FEN:       theState.game.FEN(),
		Graveyard: theState.graveyard,
	}
	b, err := json.MarshalIndent(&ss, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.fenFile, b, 0666)
}

func (s *viamChessChess) pickMove(ctx context.Context, game *chess.Game) (*chess.Move, error) {
	if s.engine == nil {
		moves := game.ValidMoves()
		if len(moves) == 0 {
			return nil, fmt.Errorf("no valid moves")
		}
		return &moves[0], nil
	}

	cmdPos := uci.CmdPosition{Position: game.Position()}
	cmdGo := uci.CmdGo{MoveTime: time.Millisecond * time.Duration(s.conf.engineMillis())}
	err := s.engine.Run(cmdPos, cmdGo)
	if err != nil {
		return nil, err
	}

	return s.engine.SearchResults().BestMove, nil

}

func (s *viamChessChess) makeAMove(ctx context.Context) (*chess.Move, error) {
	err := s.goToStart(ctx)
	if err != nil {
		return nil, fmt.Errorf("can't go home: %v", err)
	}

	theState, err := s.getGame(ctx)
	if err != nil {
		return nil, err
	}

	m, err := s.pickMove(ctx, theState.game)
	if err != nil {
		return nil, err
	}

	all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
	if err != nil {
		return nil, err
	}

	if m.HasTag(chess.KingSideCastle) || m.HasTag(chess.QueenSideCastle) {
		var f, t string
		switch m.S1().String() {
		case "e1":
			switch m.S2().String() {
			case "g1":
				f = "h1"
				t = "f1"
			case "a1":
				f = "a1"
				t = "c1"
			default:
				return nil, fmt.Errorf("bad castle? %v", m)
			}
		case "e8":
			switch m.S2().String() {
			case "g8":
				f = "h8"
				t = "f8"
			case "a8":
				f = "a8"
				t = "c8"
			default:
				return nil, fmt.Errorf("bad castle? %v", m)
			}
		default:
			return nil, fmt.Errorf("bad castle? %v", m)
		}

		err = s.movePiece(ctx, all, nil, f, t, nil)
		if err != nil {
			return nil, err
		}
	}

	if m.HasTag(chess.EnPassant) {
		return nil, fmt.Errorf("can't handle enpassant")
	}

	err = s.movePiece(ctx, all, theState, m.S1().String(), m.S2().String(), m)
	if err != nil {
		return nil, err
	}

	err = theState.game.Move(m, nil)
	if err != nil {
		return nil, err
	}

	err = s.saveGame(ctx, theState)
	if err != nil {
		return nil, err
	}

	return m, nil
}

func (s *viamChessChess) myGrab(ctx context.Context) (bool, error) {
	got, err := s.gripper.Grab(ctx, nil)
	if err != nil {
		return false, err
	}

	time.Sleep(300 * time.Millisecond)

	res, err := s.arm.DoCommand(ctx, map[string]interface{}{"get_gripper": true})
	if err != nil {
		return false, err
	}

	p, ok := res["gripper_position"].(float64)
	if !ok {
		return false, fmt.Errorf("Why is get_gripper weird %v", res)
	}

	s.logger.Debugf("gripper res: %v", res)

	if p < 20 && got {
		s.logger.Warnf("grab said we got, but i think no res: %v", res)
		return false, nil
	}

	return got, nil
}

func (s *viamChessChess) resetBoard(ctx context.Context) error {
	theMainState, err := s.getGame(ctx)
	if err != nil {
		return err
	}

	theState := &resetState{theMainState.game.Position().Board(), theMainState.graveyard}

	for {
		from, to, err := nextResetMove(theState)
		if err != nil {
			return err
		}
		if from < 0 {
			break
		}

		err = s.goToStart(ctx)
		if err != nil {
			return err
		}

		all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
		if err != nil {
			return err
		}

		err = s.movePiece(ctx, all, nil, squareToString(from), squareToString(to), nil)
		if err != nil {
			return err
		}

		err = theState.applyMove(from, to)
		if err != nil {
			return err
		}
	}

	return s.wipe(ctx)
}

func (s *viamChessChess) wipe(ctx context.Context) error {
	return os.Remove(s.fenFile)
}

func (s *viamChessChess) checkPositionForMoves(ctx context.Context) error {
	theState, err := s.getGame(ctx)
	if err != nil {
		return err
	}

	all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
	if err != nil {
		return err
	}

	differnces := []chess.Square{}
	from := chess.NoSquare
	to := chess.NoSquare

	for sq := chess.A1; sq <= chess.H8; sq++ {
		x := squareToString(sq)

		fromState := theState.game.Position().Board().Piece(sq)
		o := s.findObject(all, x)
		oc := int(o.Geometry.Label()[3] - '0')

		if int(fromState.Color()) != oc {
			s.logger.Infof("differnent %s fromState: %v o: %v oc: %v", x, fromState, o.Geometry.Label(), oc)
			differnces = append(differnces, sq)
			if oc == 0 {
				from = sq
			} else if oc > 0 {
				to = sq
			}
		}

	}

	if len(differnces) == 0 {
		return nil
	}

	if len(differnces) == 4 {
		// is this a castle??
		if squaresSame(differnces, []chess.Square{chess.E1, chess.F1, chess.G1, chess.H1}) {
			// white king castle
			from = chess.E1
			to = chess.G1
			differnces = nil
		} else if squaresSame(differnces, []chess.Square{chess.E1, chess.A1, chess.C1, chess.D1}) {
			// white queen castle
			from = chess.E1
			to = chess.C1
			differnces = nil
		} else if squaresSame(differnces, []chess.Square{chess.E8, chess.F8, chess.G8, chess.H8}) {
			// black king castle
			from = chess.E8
			to = chess.G8
			differnces = nil
		} else if squaresSame(differnces, []chess.Square{chess.E8, chess.A8, chess.C8, chess.D8}) {
			// black queen castle
			from = chess.E8
			to = chess.C8
			differnces = nil
		}
	}

	if len(differnces) != 2 && len(differnces) != 0 {
		return fmt.Errorf("bad number of differnces (%d) : %v", len(differnces), differnces)
	}

	moves := theState.game.ValidMoves()
	for _, m := range moves {
		if m.S1() == from && m.S2() == to {
			s.logger.Infof("found it: %v", m.String())
			err = theState.game.Move(&m, nil)
			if err != nil {
				return err
			}

			err = s.saveGame(ctx, theState)
			if err != nil {
				return err
			}

			return nil
		}
	}

	return fmt.Errorf("no valid moves from: %v to %v found out of %d", from, to, len(moves))
}

func (s *viamChessChess) centerCamera(ctx context.Context) error {
	err := s.goToStart(ctx)
	if err != nil {
		return nil
	}

	//pose := s.startPose.Pose()

	for {
		time.Sleep(time.Second)

		all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
		if err != nil {
			return err
		}

		sum := r3.Vector{}
		for _, pos := range []string{"d1", "e1", "d8", "e8"} {
			v, err := s.getCenterFor(all, pos, nil)
			if err != nil {
				return err
			}
			sum = sum.Add(v)
		}
		sum = sum.Mul(.25)
		s.logger.Infof("hi: %v", sum)

		return fmt.Errorf("finish me")

		/*
			if math.Abs(xDelta) < 3 && math.Abs(float64(yDelta)) < 3 {
				return nil
			}

			pose = spatialmath.NewPose(
				pose.Point().Add(r3.Vector{X: xDelta / 3, Y: yDelta / 3}),
				pose.Orientation(),
			)

			s.logger.Infof("updated pose: %v", pose)

			_, err = s.motion.Move(ctx, motion.MoveReq{
				ComponentName: s.conf.Gripper,
				Destination:   referenceframe.NewPoseInFrame("world", pose),
			})
			if err != nil {
				return fmt.Errorf("can't move to %v: %w", pose, err)
			}
		*/

	}
}

func squaresSame(a, b []chess.Square) bool {
	if len(a) != len(b) {
		return false
	}

	// Check that every element in a exists in b
	for _, sq := range a {
		found := false
		for _, sq2 := range b {
			if sq == sq2 {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
