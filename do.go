package viamchess

import (
	"context"
	"fmt"
	"time"

	"github.com/mitchellh/mapstructure"

	"go.viam.com/rdk/vision/viscapture"
	"go.viam.com/utils/trace"

	"github.com/corentings/chess/v2"
)

type MoveCmd struct {
	From, To string
	N        int
}

type cmdStruct struct {
	Move  MoveCmd
	Go    int
	Reset bool
	Wipe  bool
	Skill float64
	Hover string
}

func (s *viamChessChess) DoCommand(ctx context.Context, cmdMap map[string]interface{}) (map[string]interface{}, error) {
	s.doCommandCount.Add(1)
	ctx, span := trace.StartSpan(ctx, "chess::DoCommand")
	defer span.End()

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

	if cmd.Hover != "" {
		err := s.goToStart(ctx)
		if err != nil {
			return nil, err
		}

		all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
		if err != nil {
			return nil, err
		}

		center, err := s.getCenterFor(all, cmd.Hover, nil)
		if err != nil {
			return nil, err
		}
		center.Z = max(15, center.Z)

		err = s.setupGripper(ctx)
		if err != nil {
			return nil, err
		}

		err = s.moveGripper(ctx, center)
		if err != nil {
			return nil, err
		}

		time.Sleep(10 * time.Second)

		return map[string]interface{}{"center": center}, nil
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
		var m *chess.Move
		for n := range cmd.Go {
			m, err = s.makeAMove(ctx, n == 0)
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

	if cmd.Skill > 0 {
		s.skillAdjust = cmd.Skill
		return nil, nil
	}

	return nil, fmt.Errorf("bad cmd %v", cmdMap)
}
