package viamchess

import (
	"context"
	"fmt"
	"image"
	"strings"
	"time"

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

	return s, nil
}

func (s *viamChessChess) Name() resource.Name {
	return s.name
}

// ----

type MoveCmd struct {
	From, To string
}

type cmdStruct struct {
	Move MoveCmd
}

func (s *viamChessChess) DoCommand(ctx context.Context, cmdMap map[string]interface{}) (map[string]interface{}, error) {
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

		all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
		if err != nil {
			return nil, err
		}

		err = s.movePiece(ctx, all, cmd.Move.From, cmd.Move.To)
		if err != nil {
			return nil, err
		}

		return nil, fmt.Errorf("finish me")
	}

	return nil, fmt.Errorf("bad cmd %v", cmdMap)
}

func (s *viamChessChess) Close(context.Context) error {
	// Put close code here
	s.cancelFunc()
	return nil
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
		return r3.Vector{400, 400, 400}, nil
	}

	o := s.findObject(data, pos)
	if o == nil {
		return r3.Vector{}, fmt.Errorf("can't find object for: %s", pos)
	}

	if strings.HasSuffix(o.Geometry.Label(), "-0") {
		md := o.MetaData()
		return md.Center(), nil
	}

	return touch.PCFindHighestInRegion(o, image.Rect(-1000, -1000, 1000, 1000)), nil
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

		err = s.moveGripper(ctx, r3.Vector{center.X, center.Y, safeZ})
		if err != nil {
			return err
		}

		err = s.moveGripper(ctx, r3.Vector{center.X, center.Y, useZ})
		if err != nil {
			return err
		}

		got, err := s.gripper.Grab(ctx, nil)
		if err != nil {
			return err
		}
		if !got {
			return fmt.Errorf("Grab didn't grab")
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
	err = s.setupGripper(ctx)
	if err != nil {
		return err
	}

	time.Sleep(time.Second)

	s.startPose, err = s.rfs.GetPose(ctx, s.conf.Gripper, "world", nil, nil)
	if err != nil {
		return err
	}

	s.logger.Infof("startPose: %v", s.startPose)

	return nil
}

func (s *viamChessChess) setupGripper(ctx context.Context) error {
	_, err := s.arm.DoCommand(ctx, map[string]interface{}{"move_gripper": 450})
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
