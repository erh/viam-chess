package viamchess

import (
	"context"
	"fmt"
	"time"

	"go.viam.com/rdk/vision/viscapture"
	"go.viam.com/utils/trace"

	"github.com/corentings/chess/v2"
	"github.com/corentings/chess/v2/uci"
)

func (s *viamChessChess) pickMove(ctx context.Context, game *chess.Game) (*chess.Move, error) {
	ctx, span := trace.StartSpan(ctx, "pickMove")
	defer span.End()

	if s.engine == nil {
		moves := game.ValidMoves()
		if len(moves) == 0 {
			return nil, fmt.Errorf("no valid moves")
		}
		return &moves[0], nil
	}

	multiplier := 1.0
	if s.skillAdjust < 50 {
		multiplier = float64(s.skillAdjust) / 50.0
		s.logger.Infof("multiplier: %v", multiplier)
	} else if s.skillAdjust > 50 {
		multiplier = float64(s.skillAdjust-50) * 2
		s.logger.Infof("multiplier: %v", multiplier)
	}

	cmdPos := uci.CmdPosition{Position: game.Position()}
	cmdGo := uci.CmdGo{MoveTime: time.Millisecond * time.Duration(float64(s.conf.engineMillis())*multiplier)}
	err := s.engine.Run(cmdPos, cmdGo)
	if err != nil {
		return nil, err
	}

	return s.engine.SearchResults().BestMove, nil

}

func (s *viamChessChess) makeAMove(ctx context.Context, doSanityCheck bool) (*chess.Move, error) {
	ctx, span := trace.StartSpan(ctx, "makeAMove")
	defer span.End()

	err := s.goToStart(ctx)
	if err != nil {
		return nil, fmt.Errorf("can't go home: %v", err)
	}

	theState, err := s.getGame(ctx)
	if err != nil {
		return nil, err
	}

	all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
	if err != nil {
		return nil, err
	}

	if doSanityCheck {
		err = s.checkPositionForMoves(ctx, all)
		if err != nil {
			return nil, err
		}
	}

	m, err := s.pickMove(ctx, theState.game)
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
			case "c1":
				f = "a1"
				t = "d1"
			default:
				return nil, fmt.Errorf("bad castle? %v", m)
			}
		case "e8":
			switch m.S2().String() {
			case "g8":
				f = "h8"
				t = "f8"
			case "c8":
				f = "a8"
				t = "d8"
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

func (s *viamChessChess) checkPositionForMoves(ctx context.Context, all viscapture.VisCapture) error {
	ctx, span := trace.StartSpan(ctx, "checkPositionForMoves")
	defer span.End()

	theState, err := s.getGame(ctx)
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
		if o == nil {
			return fmt.Errorf("can't find object for square %s during position check", x)
		}
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
