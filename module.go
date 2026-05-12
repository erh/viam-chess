package viamchess

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"go.uber.org/multierr"

	"go.viam.com/rdk/components/arm"
	"go.viam.com/rdk/components/camera"
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

	name resource.Name

	logger logging.Logger
	conf   *ChessConfig

	pieceFinder vision.Service
	arm         arm.Arm
	gripper     gripper.Gripper
	cam         camera.Camera

	poseStart toggleswitch.Switch

	motion motion.Service
	rfs    framesystem.Service

	startPose   *referenceframe.PoseInFrame
	skillAdjust float64

	engine *uci.Engine

	fenFile string

	doCommandLock   sync.Mutex
	doCommandCount  atomic.Int32
	movePieceStatus atomic.Int32
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

	s := &viamChessChess{
		name:        name,
		logger:      logger,
		conf:        conf,
		skillAdjust: 50,
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
		return nil, fmt.Errorf("cannot goToStart in constructor: %w", err)
	}

	s.fenFile = filepath.Join(os.Getenv("VIAM_MODULE_DATA"), "state.json")
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

func (s *viamChessChess) Close(ctx context.Context) error {
	var err error

	if s.engine != nil {
		err = multierr.Combine(err, s.engine.Close())
	}

	return err
}
