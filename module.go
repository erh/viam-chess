package viamchess

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/multierr"

	"github.com/golang/geo/r3"

	"go.viam.com/rdk/components/arm"
	"go.viam.com/rdk/components/camera"
	componentgeneric "go.viam.com/rdk/components/generic"
	"go.viam.com/rdk/components/gripper"
	toggleswitch "go.viam.com/rdk/components/switch"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/referenceframe"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/robot/framesystem"
	generic "go.viam.com/rdk/services/generic"
	"go.viam.com/rdk/services/motion"
	"go.viam.com/rdk/services/vision"

	"github.com/corentings/chess/v2/uci"
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

type viamChessChess struct {
	resource.AlwaysRebuild
	resource.Named

	name resource.Name

	logger logging.Logger
	conf   *ChessConfig

	cancelFunc func()

	pieceFinder vision.Service
	arm         arm.Arm
	gripper     gripper.Gripper
	cam         camera.Camera
	videoSaver  resource.Resource

	poseStart toggleswitch.Switch

	motion motion.Service
	rfs    framesystem.Service

	startPose *referenceframe.PoseInFrame

	engine *uci.Engine

	fenFile string

	doCommandLock   sync.Mutex
	doCommandCount  atomic.Int32
	movePieceStatus atomic.Int32

	squareXY   map[string]r3.Vector
	squareXYMu sync.RWMutex

	// autoEnabled gates the engine reply in the board loop; detection + cache
	// refresh always run.
	autoEnabled atomic.Bool

	// announceEnabled gates the on_move_target dispatch. Default true.
	announceEnabled atomic.Bool

	// onMoveTarget receives a "move_made" domain event after every successful
	// engine move (whether triggered by cmd.Go or auto-mode). nil = disabled.
	onMoveTarget resource.Resource

	// lastScoreCP is the most recent engine evaluation in centipawns,
	// normalized to white-relative (positive = white ahead).
	// Updated after every engine move; zero before the first move or when
	// no engine is configured.
	lastScoreCP atomic.Int32
	// lastScoreMate is the most recent engine-detected moves-to-forced-mate,
	// white-relative (positive = white mates, negative = black mates, 0 = none).
	lastScoreMate atomic.Int32

	// boardCache holds the last camera-derived snapshot, populated by the
	// board loop and read by board-snapshot. Guarded by mu.
	boardCache struct {
		mu             sync.RWMutex
		ready          bool
		fen            string
		cameraBoard    map[string]interface{}
		whiteGraveyard []interface{}
		blackGraveyard []interface{}
		capturedAt     time.Time
		gameEvents     GameEventsResult
	}
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
		cancelFunc: cancelFunc,
		squareXY:   make(map[string]r3.Vector),
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

	if conf.Camera != "" {
		s.cam, err = camera.FromProvider(deps, conf.Camera)
		if err != nil {
			return nil, err
		}
	}

	if conf.VideoSaver != "" {
		s.videoSaver, err = componentgeneric.FromProvider(deps, conf.VideoSaver)
		if err != nil {
			logger.Warnf("video-saver %q not found, video recording disabled: %v", conf.VideoSaver, err)
			s.videoSaver = nil
		}
	}

	if conf.OnMoveTarget != "" {
		s.onMoveTarget, err = generic.FromProvider(deps, conf.OnMoveTarget)
		if err != nil {
			// Optional dep — log and continue. AlwaysRebuild will re-run this
			// constructor once the target becomes available, so announcements
			// turn on automatically without manual intervention.
			logger.Warnf("on_move_target %q not yet available, announcements disabled until rebuild: %v", conf.OnMoveTarget, err)
			s.onMoveTarget = nil
		}
	}
	s.announceEnabled.Store(true)

	s.poseStart, err = toggleswitch.FromProvider(deps, conf.PoseStart)
	if err != nil {
		return nil, err
	}

	s.motion, err = motion.FromProvider(deps, "builtin")
	if err != nil {
		return nil, err
	}

	s.rfs, err = framesystem.FromDependencies(deps)
	if err != nil {
		logger.Errorf("can't find framesystem: %v", err)
	}

	err = s.goToStart(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot goToStart in constructor: %w", err)
	}

	s.fenFile = filepath.Join(os.Getenv("VIAM_MODULE_DATA"), "state.json")
	s.logger.Infof("fenFile: %v", s.fenFile)
	s.engine, err = uci.New(conf.engine())
	if err != nil {
		return nil, err
	}

	go s.runBoardLoop(cancelCtx)

	err = s.engine.Run(uci.CmdUCI, uci.CmdIsReady, uci.CmdUCINewGame) // TODO: not sure this is correct
	if err != nil {
		return nil, err
	}

	if err := s.applyDifficulty(conf.initialDifficulty()); err != nil {
		s.cancelFunc()
		return nil, fmt.Errorf("applying initial difficulty: %w", err)
	}

	return s, nil
}

// applyElo sets the engine to target a specific Elo rating via UCI_LimitStrength.
// The value is clamped to the engine's reported UCI_Elo min/max with a warning.
func (s *viamChessChess) applyElo(elo int) error {
	if s.engine == nil {
		return nil
	}
	if opt, ok := s.engine.Options()["UCI_Elo"]; ok {
		if min, err := strconv.Atoi(opt.Min); err == nil && elo < min {
			s.logger.Warnf("ELO %d is below the engine minimum of %d, clamping", elo, min)
			elo = min
		}
		if max, err := strconv.Atoi(opt.Max); err == nil && elo > max {
			s.logger.Warnf("ELO %d exceeds the engine maximum of %d, clamping", elo, max)
			elo = max
		}
	}
	return s.engine.Run(
		uci.CmdSetOption{Name: "Skill Level", Value: "20"},
		uci.CmdSetOption{Name: "UCI_LimitStrength", Value: "true"},
		uci.CmdSetOption{Name: "UCI_Elo", Value: fmt.Sprintf("%d", elo)},
	)
}

// applyDifficulty sends the appropriate UCI setoption commands to the engine.
// Valid difficulty values: "beginner", "intermediate", "advanced", "expert", "impossible".
func (s *viamChessChess) applyDifficulty(difficulty string) error {
	if s.engine == nil {
		return nil
	}
	switch difficulty {
	case "beginner":
		return s.engine.Run(
			uci.CmdSetOption{Name: "UCI_LimitStrength", Value: "false"},
			uci.CmdSetOption{Name: "Skill Level", Value: "0"},
		)
	case "intermediate":
		return s.engine.Run(
			uci.CmdSetOption{Name: "Skill Level", Value: "20"},
			uci.CmdSetOption{Name: "UCI_LimitStrength", Value: "true"},
			uci.CmdSetOption{Name: "UCI_Elo", Value: "1320"},
		)
	case "advanced":
		return s.engine.Run(
			uci.CmdSetOption{Name: "Skill Level", Value: "20"},
			uci.CmdSetOption{Name: "UCI_LimitStrength", Value: "true"},
			uci.CmdSetOption{Name: "UCI_Elo", Value: "1800"},
		)
	case "expert":
		return s.engine.Run(
			uci.CmdSetOption{Name: "Skill Level", Value: "20"},
			uci.CmdSetOption{Name: "UCI_LimitStrength", Value: "true"},
			uci.CmdSetOption{Name: "UCI_Elo", Value: "2400"},
		)
	case "impossible":
		return s.engine.Run(
			uci.CmdSetOption{Name: "UCI_LimitStrength", Value: "false"},
			uci.CmdSetOption{Name: "Skill Level", Value: "20"},
		)
	default:
		s.logger.Warnf("unknown difficulty %q (must be one of: beginner, intermediate, advanced, expert, impossible) — defaulting to intermediate", difficulty)
		return s.engine.Run(
			uci.CmdSetOption{Name: "Skill Level", Value: "20"},
			uci.CmdSetOption{Name: "UCI_LimitStrength", Value: "true"},
			uci.CmdSetOption{Name: "UCI_Elo", Value: "1320"},
		)
	}
}

func (s *viamChessChess) Name() resource.Name {
	return s.name
}

func (s *viamChessChess) Close(ctx context.Context) error {
	var err error

	s.cancelFunc()

	if s.engine != nil {
		err = multierr.Combine(err, s.engine.Close())
	}

	return err
}
