package viamchess

import (
	"context"
	"fmt"

	"github.com/mitchellh/mapstructure"
	
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"
	generic "go.viam.com/rdk/services/generic"
)

var ChessModel = family.WithModel("chess")

func init() {
	resource.RegisterService(generic.API, ChessModel,
		resource.Registration[resource.Resource, *ChessConfig]{
			Constructor: newViamChessChess,
		},
	)
}

type ChessConfig struct {
	PieceFinder string `json:"piece-finder"`
}

func (cfg *ChessConfig) Validate(path string) ([]string, []string, error) {
	if cfg.PieceFinder == "" {
		return nil, nil, fmt.Errorf("need a piece-finder")
	}
	return []string{cfg.PieceFinder}, nil, nil
}

type viamChessChess struct {
	resource.AlwaysRebuild

	name resource.Name

	logger logging.Logger
	cfg    *ChessConfig

	cancelCtx  context.Context
	cancelFunc func()
}

func newViamChessChess(ctx context.Context, deps resource.Dependencies, rawConf resource.Config, logger logging.Logger) (resource.Resource, error) {
	conf, err := resource.NativeConfig[*ChessConfig](rawConf)
	if err != nil {
		return nil, err
	}

	return NewChess(ctx, deps, rawConf.ResourceName(), conf, logger)

}

func NewChess(ctx context.Context, deps resource.Dependencies, name resource.Name, conf *ChessConfig, logger logging.Logger) (resource.Resource, error) {

	cancelCtx, cancelFunc := context.WithCancel(context.Background())

	s := &viamChessChess{
		name:       name,
		logger:     logger,
		cfg:        conf,
		cancelCtx:  cancelCtx,
		cancelFunc: cancelFunc,
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
	var cmd cmdStruct
	err := mapstructure.Decode(cmdMap, &cmd)
	if err != nil {
		return nil, err
	}

	if cmd.Move.To != "" && cmd.Move.From != "" {
		s.logger.Infof("move %v to %v", cmd.Move.From, cmd.Move.To)
		
		return nil, fmt.Errorf("finish me")
	}
	
	return nil, fmt.Errorf("unknown cmd %v", cmd)
}

func (s *viamChessChess) Close(context.Context) error {
	// Put close code here
	s.cancelFunc()
	return nil
}
