package viamchess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/corentings/chess/v2"
	"go.viam.com/utils/trace"
)

type state struct {
	game           *chess.Game
	whiteGraveyard []int // a-file side
	blackGraveyard []int // h-file side
}

type savedState struct {
	FEN            string   `json:"fen,omitempty"`
	Moves          []string `json:"moves,omitempty"`
	WhiteGraveyard []int    `json:"white_graveyard,omitempty"`
	BlackGraveyard []int    `json:"black_graveyard,omitempty"`
}

func (s *viamChessChess) getGame(ctx context.Context) (*state, error) {
	return readState(ctx, s.fenFile)
}

func readState(ctx context.Context, fn string) (*state, error) {
	ctx, span := trace.StartSpan(ctx, "readState")
	defer span.End()

	data, err := os.ReadFile(fn)
	if os.IsNotExist(err) {
		return &state{game: chess.NewGame()}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("error reading fen (%s): %w", fn, err)
	}

	ss := savedState{}
	err = json.Unmarshal(data, &ss)
	if err != nil {
		return nil, fmt.Errorf("cannot unmarshal json: %w", err)
	}

	if len(ss.Moves) > 0 {
		game := chess.NewGame()
		for i, moveStr := range ss.Moves {
			if err := game.PushNotationMove(moveStr, chess.UCINotation{}, nil); err != nil {
				return nil, fmt.Errorf("cannot replay move %d (%s): %w", i, moveStr, err)
			}
		}
		return &state{game: game, whiteGraveyard: ss.WhiteGraveyard, blackGraveyard: ss.BlackGraveyard}, nil
	}

	// Legacy: FEN-only state (no move history → no undo).
	f, err := chess.FEN(ss.FEN)
	if err != nil {
		return nil, fmt.Errorf("invalid fen from (%s) (%s) %w", fn, data, err)
	}
	return &state{game: chess.NewGame(f), whiteGraveyard: ss.WhiteGraveyard, blackGraveyard: ss.BlackGraveyard}, nil
}

// parseFENPiece maps a FEN piece letter (uppercase white, lowercase black) to
// its chess.Piece. Kings are excluded — they can't be captured, so they never
// belong in a graveyard.
func parseFENPiece(s string) (chess.Piece, bool) {
	switch s {
	case "P":
		return chess.WhitePawn, true
	case "N":
		return chess.WhiteKnight, true
	case "B":
		return chess.WhiteBishop, true
	case "R":
		return chess.WhiteRook, true
	case "Q":
		return chess.WhiteQueen, true
	case "p":
		return chess.BlackPawn, true
	case "n":
		return chess.BlackKnight, true
	case "b":
		return chess.BlackBishop, true
	case "r":
		return chess.BlackRook, true
	case "q":
		return chess.BlackQueen, true
	}
	return chess.NoPiece, false
}

func pieceIntToFEN(p int) string {
	piece := chess.Piece(p)
	if piece == chess.NoPiece {
		return ""
	}
	s := piece.Type().String()
	if piece.Color() == chess.White {
		return strings.ToUpper(s)
	}
	return s
}

func (s *viamChessChess) saveGame(ctx context.Context, theState *state) error {
	ctx, span := trace.StartSpan(ctx, "saveGame")
	defer span.End()

	gameMoves := theState.game.Moves()
	moveStrs := make([]string, len(gameMoves))
	for i, m := range gameMoves {
		moveStrs[i] = m.String()
	}

	ss := savedState{
		FEN:            theState.game.FEN(),
		Moves:          moveStrs,
		WhiteGraveyard: theState.whiteGraveyard,
		BlackGraveyard: theState.blackGraveyard,
	}
	b, err := json.MarshalIndent(&ss, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.fenFile, b, 0666)
}

func (s *viamChessChess) wipe(ctx context.Context) error {
	err := os.Remove(s.fenFile)
	if errors.Is(err, os.ErrNotExist) {
		s.logger.Warnf("wipe called but no game state file found at %s — nothing to wipe", s.fenFile)
		return nil
	}
	return err
}

// ensureNoGame removes the saved game if present, quietly. It is the idempotent
// "no game in START" enforcer the loop runs each START tick (and the START ->
// active transition runs inline), so START always means no game. Unlike wipe it
// does not warn on a missing file, since absence is the normal case.
func (s *viamChessChess) ensureNoGame() error {
	err := os.Remove(s.fenFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
