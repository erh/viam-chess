package viamchess

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/golang/geo/r3"

	"github.com/corentings/chess/v2"

	"go.viam.com/rdk/motionplan"
	"go.viam.com/rdk/referenceframe"
	"go.viam.com/rdk/services/motion"
	"go.viam.com/rdk/spatialmath"
	"go.viam.com/rdk/vision/viscapture"
	"go.viam.com/utils/trace"
)

func (s *viamChessChess) movePiece(ctx context.Context, data viscapture.VisCapture, theState *state, from, to string, m *chess.Move, board *chess.Board) error {
	return s.movePieceWithPickupZ(ctx, data, theState, from, to, m, board, 0)
}

func (s *viamChessChess) pickupZForPieceType(pt chess.PieceType) float64 {
	if pt == chess.King || pt == chess.Queen {
		return s.conf.grabZTall()
	}
	return s.conf.grabZ()
}

// pickupZOverride <= 0 means auto-detect from theState/board (matches movePiece).
// Pass a positive value when the source isn't expressible as a chess.Board square
// (e.g., a graveyard slot during undo).
func (s *viamChessChess) movePieceWithPickupZ(ctx context.Context, data viscapture.VisCapture, theState *state, from, to string, m *chess.Move, board *chess.Board, pickupZOverride float64) error {
	s.movePieceStatus.Add(1)
	defer s.movePieceStatus.Add(-1)

	ctx, span := trace.StartSpan(ctx, "movePiece")
	defer span.End()

	s.logger.Infof("movePiece called: %s -> %s", from, to)
	if to != "-" && to[0] != 'X' {
		occupied := false
		var capturedPiece chess.Piece
		if theState != nil {
			sq := chess.NewSquare(chess.File(to[0]-'a'), chess.Rank(to[1]-'1'))
			capturedPiece = theState.game.Position().Board().Piece(sq)
			occupied = capturedPiece != chess.NoPiece
		} else if len(data.Objects) > 0 {
			o := s.findObject(data, to)
			if o == nil {
				return fmt.Errorf("can't find object for: %s", to)
			}
			occupied = !strings.HasSuffix(o.Geometry.Label(), "-0")
		}

		if occupied {
			s.logger.Infof("position %s already has a piece, will move to graveyard", to)
			err := s.movePiece(ctx, data, theState, to, "-", nil, nil)
			if err != nil {
				return fmt.Errorf("can't move piece out of the way: %w", err)
			}
			if theState != nil {
				if capturedPiece.Color() == chess.White {
					theState.whiteGraveyard = append(theState.whiteGraveyard, int(capturedPiece))
				} else {
					theState.blackGraveyard = append(theState.blackGraveyard, int(capturedPiece))
				}
			}
		}
	}

	grabZ := s.conf.grabZ()
	grabZTall := s.conf.grabZTall()

	var pieceBoard *chess.Board
	if theState != nil {
		pieceBoard = theState.game.Position().Board()
	} else if board != nil {
		pieceBoard = board
	}

	pickupZ := grabZ
	if pickupZOverride > 0 {
		pickupZ = pickupZOverride
	} else {
		if pieceBoard != nil && len(from) == 2 {
			sq := chess.NewSquare(chess.File(from[0]-'a'), chess.Rank(from[1]-'1'))
			pt := pieceBoard.Piece(sq).Type()
			if pt == chess.King || pt == chess.Queen {
				pickupZ = grabZTall
			}
		}
		// extraQueenGraveyardSlot always holds a queen (see promotion.go).
		if from == fmt.Sprintf("XW%d", extraQueenGraveyardSlot) || from == fmt.Sprintf("XB%d", extraQueenGraveyardSlot) {
			pickupZ = grabZTall
		}
	}

	{
		xy, err := s.getSquareXY(from, data)
		if err != nil {
			return err
		}

		err = s.setupGripper(ctx)
		if err != nil {
			return err
		}

		err = s.moveGripper(ctx, r3.Vector{X: xy.X, Y: xy.Y, Z: safeZ})
		if err != nil {
			return err
		}

		grabPos := r3.Vector{X: xy.X, Y: xy.Y, Z: pickupZ}

		tryGrab := func(pos r3.Vector) (bool, error) {
			if err := s.setupGripper(ctx); err != nil {
				return false, err
			}
			time.Sleep(500 * time.Millisecond)
			if err := s.moveGripperWithTheta(ctx, pos, pickupThetaFor(pieceBoard, from)); err != nil {
				return false, err
			}
			return s.myGrab(ctx)
		}

		got, err := tryGrab(grabPos)
		if err != nil {
			return err
		}
		if !got {
			s.logger.Warnf("grab failed at %s, retrying +20mm X", from)
			got, err = tryGrab(r3.Vector{X: grabPos.X + 20, Y: grabPos.Y, Z: grabPos.Z})
			if err != nil {
				return err
			}
		}
		if !got {
			return fmt.Errorf("couldn't grab piece at %s after 2 attempts", from)
		}

		err = s.moveGripper(ctx, r3.Vector{X: xy.X, Y: xy.Y, Z: safeZ})
		if err != nil {
			return err
		}
	}

	{
		var destXY r3.Vector
		if to == "-" {
			// Slot 0 is reserved for the promotion spare queen; captures use 1+.
			// Without theState we can read color from the camera label
			// ("<sq>-1" = white, "<sq>-2" = black) but not the slot count, so we
			// default to slot 1 — cmd.Go is the bookkeeper for accumulation.
			colorIdx, isWhite := 1, false
			if theState != nil && len(from) == 2 {
				sq := chess.NewSquare(chess.File(from[0]-'a'), chess.Rank(from[1]-'1'))
				piece := theState.game.Position().Board().Piece(sq)
				isWhite = piece.Color() == chess.White
				if isWhite {
					colorIdx = len(theState.whiteGraveyard) + 1
				} else {
					colorIdx = len(theState.blackGraveyard) + 1
				}
			} else if len(from) == 2 && len(data.Objects) > 0 {
				if o := s.findObject(data, from); o != nil {
					label := o.Geometry.Label()
					if !strings.HasSuffix(label, "-0") && len(label) > 0 {
						switch label[len(label)-1] {
						case '1':
							isWhite = true
						case '2':
							isWhite = false
						}
					}
				}
			}
			center, err := s.graveyardPosition(data, colorIdx, isWhite)
			if err != nil {
				return err
			}
			destXY = r3.Vector{X: center.X, Y: center.Y}
		} else if len(to) > 0 && to[0] == 'X' {
			center, err := s.getCenterFor(data, to, theState)
			if err != nil {
				return err
			}
			destXY = r3.Vector{X: center.X, Y: center.Y}
		} else {
			var err error
			destXY, err = s.getSquareXY(to, data)
			if err != nil {
				return err
			}
		}

		err := s.moveGripper(ctx, r3.Vector{X: destXY.X, Y: destXY.Y, Z: safeZ})
		if err != nil {
			return err
		}

		err = s.moveGripperWithTheta(ctx, r3.Vector{X: destXY.X, Y: destXY.Y, Z: pickupZ}, pickupThetaFor(pieceBoard, to))
		if err != nil {
			return err
		}

		err = s.setupGripper(ctx)
		if err != nil {
			return err
		}

		err = s.moveGripper(ctx, r3.Vector{X: destXY.X, Y: destXY.Y, Z: safeZ})
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

	_, err := s.arm.DoCommand(ctx, map[string]interface{}{"move_gripper": s.conf.gripperOpenPos()})
	return err
}

// pickupThetaOffset is added to the wrist (6th joint) Theta when descending to
// grab or place a piece. The gripper points straight down (OZ:-1), so Theta
// spins the final joint about vertical.
const pickupThetaOffset = 25

// pickupThetaFor returns pickupThetaOffset only when sq is in the highest four
// ranks (5-8) AND an adjacent-file square on the same rank holds a tall piece
// (King or Queen) that the wrist must spin to clear. Nearer ranks, non-board
// targets (graveyard "-", "X..." slots), and a nil board all use 0.
func pickupThetaFor(board *chess.Board, sq string) float64 {
	if len(sq) != 2 || sq[1] < '5' || sq[1] > '8' || board == nil {
		return 0
	}
	file := int(sq[0] - 'a')
	rank := chess.Rank(sq[1] - '1')
	for _, df := range []int{-1, 1} {
		f := file + df
		if f < 0 || f > 7 {
			continue
		}
		pt := board.Piece(chess.NewSquare(chess.File(f), rank)).Type()
		if pt == chess.King || pt == chess.Queen {
			return pickupThetaOffset
		}
	}
	return 0
}

func (s *viamChessChess) moveGripper(ctx context.Context, p r3.Vector) error {
	return s.moveGripperWithTheta(ctx, p, 0)
}

func (s *viamChessChess) moveGripperWithTheta(ctx context.Context, p r3.Vector, thetaOffset float64) error {
	ctx, span := trace.StartSpan(ctx, "moveGripper")
	defer span.End()

	orientation := &spatialmath.OrientationVectorDegrees{
		OZ:    -1,
		Theta: s.startPose.Pose().Orientation().OrientationVectorDegrees().Theta - 180 + thetaOffset,
	}

	if p.X > 300 {
		orientation.OX = (p.X - 300) / 1000
	}

	if p.Y < -300 {
		orientation.OY = (p.Y + 300) / 300
		orientation.OX += .2
	}

	myPose := spatialmath.NewPose(p, orientation)
	myConstraints := &motionplan.Constraints{}
	myConstraints.AddOrientationConstraint(motionplan.OrientationConstraint{OrientationToleranceDegs: 45})
	_, err := s.motion.Move(ctx, motion.MoveReq{
		ComponentName: s.conf.Gripper,
		Destination:   referenceframe.NewPoseInFrame("world", myPose),
		Constraints:   myConstraints,
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
