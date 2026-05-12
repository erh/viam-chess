package viamchess

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/golang/geo/r3"

	"go.viam.com/rdk/referenceframe"
	"go.viam.com/rdk/services/motion"
	"go.viam.com/rdk/spatialmath"
	"go.viam.com/rdk/vision/viscapture"
	"go.viam.com/utils/trace"

	"github.com/corentings/chess/v2"
)

func (s *viamChessChess) movePiece(ctx context.Context, data viscapture.VisCapture, theState *state, from, to string, m *chess.Move) error {
	s.movePieceStatus.Add(1)
	defer s.movePieceStatus.Add(-1)

	ctx, span := trace.StartSpan(ctx, "movePiece")
	defer span.End()

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
				if pc.Color() == chess.White {
					theState.whiteGraveyard = append(theState.whiteGraveyard, int(pc))
				} else {
					theState.blackGraveyard = append(theState.blackGraveyard, int(pc))
				}
			}

		}
	}

	useZ := 100.0

	const magicMin = 12.0
	{
		center, err := s.getCenterFor(data, from, theState)
		if err != nil {
			return err
		}
		useZ = max(magicMin, center.Z) // HACK 5 should not be there

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
			if useZ < magicMin { // todo: magic number
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
		var center r3.Vector
		var err error
		if to == "-" {
			// Pick the right graveyard from the piece's color. Without theState
			// we fall back to the generic getCenterFor "-" position.
			isWhite := false
			colorIdx := 0
			if theState != nil && len(from) == 2 {
				sq := chess.NewSquare(chess.File(from[0]-'a'), chess.Rank(from[1]-'1'))
				piece := theState.game.Position().Board().Piece(sq)
				isWhite = piece.Color() == chess.White
				if isWhite {
					colorIdx = len(theState.whiteGraveyard)
				} else {
					colorIdx = len(theState.blackGraveyard)
				}
			}
			center, err = s.graveyardPosition(data, colorIdx, isWhite)
		} else {
			center, err = s.getCenterFor(data, to, theState)
		}
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
	ctx, span := trace.StartSpan(ctx, "goToStart")
	defer span.End()

	err := s.poseStart.SetPosition(ctx, 2, nil)
	if err != nil {
		return err
	}
	err = s.gripper.Open(ctx, nil)
	if err != nil {
		return err
	}

	time.Sleep(time.Millisecond * 250)

	s.startPose, err = s.rfs.GetPose(ctx, s.conf.Gripper, "world", nil, nil)
	if err != nil {
		return err
	}

	return nil
}

func (s *viamChessChess) setupGripper(ctx context.Context) error {
	ctx, span := trace.StartSpan(ctx, "setupGripper")
	defer span.End()

	_, err := s.arm.DoCommand(ctx, map[string]interface{}{"move_gripper": 450.0})
	return err
}

func (s *viamChessChess) moveGripper(ctx context.Context, p r3.Vector) error {
	ctx, span := trace.StartSpan(ctx, "moveGripper")
	defer span.End()

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
