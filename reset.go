package viamchess

import (
	"context"
	"fmt"
	"strings"

	"github.com/corentings/chess/v2"

	"go.viam.com/rdk/vision/viscapture"
)

var homeRanks = []chess.Rank{chess.Rank1, chess.Rank2, chess.Rank8, chess.Rank7}

type resetState struct {
	board          *chess.Board
	whiteGraveyard []int // squares 70–84
	blackGraveyard []int // squares 85–99
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
	// chess.NoSquare.String() panics; render a sentinel so error messages survive.
	if s == chess.NoSquare {
		return "<none>"
	}
	// Slot 0 is the spare queen; slice index i → physical slot i+1.
	if s >= 85 {
		return fmt.Sprintf("XB%d", int(s)-85+1)
	}
	if s >= 70 {
		return fmt.Sprintf("XW%d", int(s)-70+1)
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

	// Cache is stale after a game.
	s.clearSquareCache()

	err = s.goToStart(ctx)
	if err != nil {
		return err
	}
	all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
	if err != nil {
		return err
	}
	s.populateCacheFromCapture(all)

	// Restock the spare queen before the normal reset loop runs.
	if err := s.restoreExtraQueens(ctx, all, theState); err != nil {
		return err
	}

	for {
		from, to, err := nextResetMove(theState)
		if err != nil {
			return err
		}
		if from < 0 {
			break
		}

		fromStr := squareToString(from)
		// movePiece's default path infers grabZ/grabZTall by looking up the
		// piece type from the board, but only when `from` is a 2-char board
		// square. Graveyard sources ("XW{n}"/"XB{n}") fall through to plain
		// grabZ, so a captured king or queen would be picked up at the wrong
		// height during reset. Compute the override here from the resetState's
		// graveyard slice and pass it explicitly. (Board-source pickups keep
		// the default lookup — we only override for graveyard origins.)
		pickupZOverride := 0.0
		switch {
		case from >= 70 && from < 85:
			idx := int(from) - 70
			if idx < len(theState.whiteGraveyard) && theState.whiteGraveyard[idx] >= 0 {
				pickupZOverride = s.pickupZForPieceType(chess.Piece(theState.whiteGraveyard[idx]).Type())
			}
		case from >= 85:
			idx := int(from) - 85
			if idx < len(theState.blackGraveyard) && theState.blackGraveyard[idx] >= 0 {
				pickupZOverride = s.pickupZForPieceType(chess.Piece(theState.blackGraveyard[idx]).Type())
			}
		}
		err = s.movePieceWithPickupZ(ctx, all, nil, fromStr, squareToString(to), nil, theState.board, pickupZOverride)
		if err != nil {
			return err
		}

		// Stamp source empty so subsequent movePiece reads aren't stale.
		if from < 70 {
			for _, o := range all.Objects {
				if strings.HasPrefix(o.Geometry.Label(), fromStr+"-") {
					o.Geometry.SetLabel(fromStr + "-0")
					break
				}
			}
		}

		err = theState.applyMove(from, to)
		if err != nil {
			return err
		}
	}

	return s.wipe(ctx)
}

// v1 supports one promotion per side (single reserve slot).
func (s *viamChessChess) restoreExtraQueens(ctx context.Context, all viscapture.VisCapture, theState *resetState) error {
	for _, color := range []chess.Color{chess.White, chess.Black} {
		queens := findQueenSquares(theState.board, color)
		if len(queens) <= 1 {
			continue
		}
		extraSq := pickExtraQueen(queens, color)

		slot := fmt.Sprintf("XW%d", extraQueenGraveyardSlot)
		if color == chess.Black {
			slot = fmt.Sprintf("XB%d", extraQueenGraveyardSlot)
		}

		if err := s.movePiece(ctx, all, nil, extraSq.String(), slot, nil, theState.board); err != nil {
			return fmt.Errorf("restore extra %v queen from %s to %s: %w", color, extraSq, slot, err)
		}

		m := theState.board.SquareMap()
		m[extraSq] = chess.NoPiece
		theState.board = chess.NewBoard(m)

		sqStr := extraSq.String()
		for _, o := range all.Objects {
			if strings.HasPrefix(o.Geometry.Label(), sqStr+"-") {
				o.Geometry.SetLabel(sqStr + "-0")
				break
			}
		}
	}
	return nil
}

func findQueenSquares(b *chess.Board, color chess.Color) []chess.Square {
	var out []chess.Square
	for sq := chess.A1; sq <= chess.H8; sq++ {
		p := b.Piece(sq)
		if p.Type() == chess.Queen && p.Color() == color {
			out = append(out, sq)
		}
	}
	return out
}

// Prefer the off-home queen (the promoted one); fall back to either when both
// are off-home or both share the home square.
func pickExtraQueen(queens []chess.Square, color chess.Color) chess.Square {
	home := chess.D1
	if color == chess.Black {
		home = chess.D8
	}
	for _, sq := range queens {
		if sq != home {
			return sq
		}
	}
	return queens[0]
}
