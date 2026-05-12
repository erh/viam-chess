package viamchess

import (
	"context"
	"fmt"

	"github.com/corentings/chess/v2"

	"go.viam.com/rdk/vision/viscapture"
)

var homeRanks = []chess.Rank{chess.Rank1, chess.Rank2, chess.Rank7, chess.Rank8}

type resetState struct {
	board          *chess.Board
	whiteGraveyard []int // sourced from chess.Square 70–84
	blackGraveyard []int // sourced from chess.Square 85–99
}

func (s *resetState) applyMove(from, to chess.Square) error {
	m := s.board.SquareMap()
	switch {
	case from < 70:
		m[to] = m[from]
		m[from] = chess.NoPiece
	case from < 85:
		idx := int(from) - 70
		m[to] = chess.Piece(s.whiteGraveyard[idx])
		s.whiteGraveyard[idx] = -1
	default:
		idx := int(from) - 85
		m[to] = chess.Piece(s.blackGraveyard[idx])
		s.blackGraveyard[idx] = -1
	}
	s.board = chess.NewBoard(m)
	return nil
}

func squareToString(s chess.Square) string {
	if s >= 85 {
		return fmt.Sprintf("XB%d", int(s)-85)
	}
	if s >= 70 {
		return fmt.Sprintf("XW%d", int(s)-70)
	}
	return s.String()
}

func findForRest(theState *resetState, correct *chess.Board, what chess.Piece) (chess.Square, error) {
	for _, r := range []chess.Rank{
		chess.Rank1, chess.Rank2, chess.Rank7, chess.Rank8,
		chess.Rank3, chess.Rank4, chess.Rank5, chess.Rank6} {

		for f := chess.FileA; f <= chess.FileH; f++ {
			sq := chess.NewSquare(f, r)
			have := theState.board.Piece(sq)
			if have != what {
				continue
			}
			good := correct.Piece(sq)
			if good == have {
				continue
			}
			return sq, nil
		}
	}

	for idx, p := range theState.whiteGraveyard {
		if what == chess.Piece(p) {
			return chess.Square(70 + idx), nil
		}
	}
	for idx, p := range theState.blackGraveyard {
		if what == chess.Piece(p) {
			return chess.Square(85 + idx), nil
		}
	}

	return chess.A1, fmt.Errorf("cannot find a %v", what)
}

func nextResetMove(theState *resetState) (chess.Square, chess.Square, error) {
	// first look for empty home squares

	correct := chess.NewGame().Position().Board()

	for _, r := range homeRanks {
		for f := chess.FileA; f <= chess.FileH; f++ {
			sq := chess.NewSquare(f, r)

			have := theState.board.Piece(sq)
			good := correct.Piece(sq)

			if have == chess.NoPiece {
				from, err := findForRest(theState, correct, good)
				if err != nil {
					return chess.A1, chess.A1, err
				}
				return from, sq, nil
			}

		}
	}

	return -1, -1, nil
}

func (s *viamChessChess) resetBoard(ctx context.Context) error {
	theMainState, err := s.getGame(ctx)
	if err != nil {
		return err
	}

	theState := &resetState{
		board:          theMainState.game.Position().Board(),
		whiteGraveyard: theMainState.whiteGraveyard,
		blackGraveyard: theMainState.blackGraveyard,
	}

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
