package viamchess

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"go.viam.com/utils/trace"

	"github.com/corentings/chess/v2"
)

type state struct {
	game      *chess.Game
	graveyard []int
}

type savedState struct {
	FEN       string `json:"fen"`
	Graveyard []int  `json:"graveyard"`
}

func (s *viamChessChess) getGame(ctx context.Context) (*state, error) {
	return readState(ctx, s.fenFile)
}

func readState(ctx context.Context, fn string) (*state, error) {
	ctx, span := trace.StartSpan(ctx, "readState")
	defer span.End()

	data, err := os.ReadFile(fn)
	if os.IsNotExist(err) {
		return &state{chess.NewGame(), []int{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("error reading fen (%s): %w", fn, err)
	}

	ss := savedState{}
	err = json.Unmarshal(data, &ss)
	if err != nil {
		return nil, fmt.Errorf("cannot unmarshal json: %w", err)
	}

	f, err := chess.FEN(ss.FEN)
	if err != nil {
		return nil, fmt.Errorf("invalid fen from (%s) (%s) %w", fn, data, err)
	}
	return &state{chess.NewGame(f), ss.Graveyard}, nil
}

func (s *viamChessChess) saveGame(ctx context.Context, theState *state) error {
	ctx, span := trace.StartSpan(ctx, "saveGame")
	defer span.End()

	ss := savedState{
		FEN:       theState.game.FEN(),
		Graveyard: theState.graveyard,
	}
	b, err := json.MarshalIndent(&ss, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.fenFile, b, 0666)
}

func (s *viamChessChess) wipe(ctx context.Context) error {
	return os.Remove(s.fenFile)
}
