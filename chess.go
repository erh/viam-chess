package viamchess

import (
	"context"
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

	return []string{cfg.PieceFinder, cfg.Arm, cfg.Gripper, cfg.PoseStart}, nil, nil
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

	s.fenFile = os.Getenv("VIAM_MODULE_DATA") + "fen.txt"
	s.logger.Infof("fenFile: %v", s.fenFile)
	s.engine, err = uci.New("stockfish")
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
	Move MoveCmd
	Go   int
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

			err = s.movePiece(ctx, all, from, to)
			if err != nil {
				return nil, err
			}
		}

		return nil, nil
	}

	if cmd.Go > 0 {
		var m *chess.Move
		for range cmd.Go {
			m, err = s.makeAMove(ctx)
			if err != nil {
				return nil, err
			}
		}
		return map[string]interface{}{"move": m.String()}, nil
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

func (s *viamChessChess) getCenterFor(data viscapture.VisCapture, pos string) (r3.Vector, error) {
	if pos == "-" {
		return r3.Vector{400, -400, 400}, nil
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

func (s *viamChessChess) movePiece(ctx context.Context, data viscapture.VisCapture, from, to string) error {
	s.logger.Infof("movePiece called: %s -> %s", from, to)
	if to != "-" { // check where we're going
		o := s.findObject(data, to)
		if o == nil {
			return fmt.Errorf("can't find object for: %s", to)
		}

		if !strings.HasSuffix(o.Geometry.Label(), "-0") {
			s.logger.Infof("position %s already has a piece (%s), will move", to, o.Geometry.Label())
			err := s.movePiece(ctx, data, to, "-")
			if err != nil {
				return fmt.Errorf("can't move piece out of the way: %w", err)
			}
		}
	}

	useZ := 100.0

	{
		center, err := s.getCenterFor(data, from)
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
			s.logger.Warnf("didn't grab, going to try a little more")
			useZ -= 10
			if useZ < 0 { // todo: magic number
				return fmt.Errorf("couldn't grab, and scared to go lower")
			}

			err = s.setupGripper(ctx)
			if err != nil {
				return err
			}

		}

		err = s.moveGripper(ctx, r3.Vector{center.X, center.Y, safeZ})
		if err != nil {
			return err
		}
	}

	{
		center, err := s.getCenterFor(data, to)
		if err != nil {
			return err
		}

		err = s.moveGripper(ctx, r3.Vector{center.X, center.Y, safeZ})
		if err != nil {
			return err
		}

		if to == "-" { // TODO: temp hack
			useZ = 300
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

	_, err := s.motion.Move(ctx, motion.MoveReq{
		ComponentName: s.conf.Gripper,
		Destination: referenceframe.NewPoseInFrame("world",
			spatialmath.NewPose(
				p,
				orientation,
			)),
	})
	return err
}

func (s *viamChessChess) getGame(ctx context.Context) (*chess.Game, error) {
	data, err := os.ReadFile(s.fenFile)
	if os.IsNotExist(err) {
		return chess.NewGame(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("error reading fen (%s) %T", s.fenFile, err)
	}
	f, err := chess.FEN(string(data))
	if err != nil {
		return nil, fmt.Errorf("invalid fen from (%s) (%s) %w", s.fenFile, data, err)
	}
	return chess.NewGame(f), nil
}

func (s *viamChessChess) saveGame(ctx context.Context, g *chess.Game) error {
	return os.WriteFile(s.fenFile, []byte(g.FEN()), 0666)
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
	cmdGo := uci.CmdGo{MoveTime: time.Second / 100}
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

	game, err := s.getGame(ctx)
	if err != nil {
		return nil, err
	}

	m, err := s.pickMove(ctx, game)
	if err != nil {
		return nil, err
	}

	if m.HasTag(chess.KingSideCastle) || m.HasTag(chess.QueenSideCastle) {
		return nil, fmt.Errorf("can't handle castle %v", m)
	}

	all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
	if err != nil {
		return nil, err
	}

	err = s.movePiece(ctx, all, m.S1().String(), m.S2().String())
	if err != nil {
		return nil, err
	}

	err = game.Move(m, nil)
	if err != nil {
		return nil, err
	}

	err = s.saveGame(ctx, game)
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
