package viamchess

import (
	"context"
	"strings"
	"testing"
)

// TestSetBoardModeGating locks in the rejected modes: START (its tick wipes
// state.json, so the write would be silently lost) and VS_SELF (the loop would
// immediately play over the injected position). Both reject before touching
// any state, so a zero-value service with just the mode set is enough.
func TestSetBoardModeGating(t *testing.T) {
	for _, tc := range []struct {
		mode Mode
		want string
	}{
		{ModeStart, "START"},
		{ModeVsSelf, "VS_SELF"},
	} {
		s := &viamChessChess{}
		s.mode.mode = tc.mode
		err := s.setBoard(context.Background(), &SetBoardCmd{FEN: "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"})
		if err == nil {
			t.Errorf("setBoard in %v: expected rejection, got nil", tc.mode)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("setBoard in %v: error %q does not mention %q", tc.mode, err.Error(), tc.want)
		}
	}
}

// TestParseFENPiece checks the letter <-> piece mapping round-trips through
// pieceIntToFEN (graveyards store pieces as ints) and that kings and junk are
// rejected.
func TestParseFENPiece(t *testing.T) {
	for _, letter := range []string{"P", "N", "B", "R", "Q", "p", "n", "b", "r", "q"} {
		piece, ok := parseFENPiece(letter)
		if !ok {
			t.Errorf("parseFENPiece(%q): expected ok", letter)
			continue
		}
		if got := pieceIntToFEN(int(piece)); got != letter {
			t.Errorf("parseFENPiece(%q) round-trip: got %q", letter, got)
		}
	}
	for _, bad := range []string{"K", "k", "x", "", "PP"} {
		if _, ok := parseFENPiece(bad); ok {
			t.Errorf("parseFENPiece(%q): expected rejection", bad)
		}
	}
}
