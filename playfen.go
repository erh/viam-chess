package viamchess

import (
	"context"
	"fmt"
	"os"

	"github.com/corentings/chess/v2"

	"go.viam.com/rdk/vision/viscapture"
)

// Wipes state, then physically replays every move from the PGN.
func (s *viamChessChess) playFENFile(ctx context.Context, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("cannot open FEN file %s: %w", path, err)
	}
	defer f.Close()

	pgnFunc, err := chess.PGN(f)
	if err != nil {
		return fmt.Errorf("cannot parse PGN from %s: %w", path, err)
	}

	parsedGame := chess.NewGame(pgnFunc)
	moves := parsedGame.Moves()
	s.logger.Infof("playFENFile: loaded %d moves from %s", len(moves), path)

	if err := s.wipe(ctx); err != nil {
		return fmt.Errorf("wipe before playFENFile: %w", err)
	}

	theState := &state{chess.NewGame(), []int{}, []int{}}

	err = s.goToStart(ctx)
	if err != nil {
		return err
	}
	all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
	if err != nil {
		return err
	}
	s.populateCacheFromCapture(all)

	for i, m := range moves {
		s.logger.Infof("playFENFile: move %d/%d: %s", i+1, len(moves), m.String())

		if m.HasTag(chess.KingSideCastle) || m.HasTag(chess.QueenSideCastle) {
			var f2, t2 string
			switch m.S1().String() {
			case "e1":
				if m.S2().String() == "g1" {
					f2, t2 = "h1", "f1"
				} else {
					f2, t2 = "a1", "d1"
				}
			case "e8":
				if m.S2().String() == "g8" {
					f2, t2 = "h8", "f8"
				} else {
					f2, t2 = "a8", "d8"
				}
			default:
				return fmt.Errorf("bad castle? %v", m)
			}
			if err := s.movePiece(ctx, all, theState, f2, t2, nil, nil); err != nil {
				return err
			}
		}

		if m.HasTag(chess.EnPassant) {
			startRank := m.S1().String()[1]
			endFile := m.S2().String()[0]
			pieceToRemove := fmt.Sprintf("%c%c", endFile, startRank)
			if err := s.movePiece(ctx, all, theState, pieceToRemove, "-", nil, nil); err != nil {
				return err
			}
			if startRank == '5' {
				theState.blackGraveyard = append(theState.blackGraveyard, 12)
			} else {
				theState.whiteGraveyard = append(theState.whiteGraveyard, 6)
			}
		}

		if m.Promo() != chess.NoPieceType {
			if err := s.handlePromotionMove(ctx, all, theState, m); err != nil {
				return fmt.Errorf("playFENFile promote move %d (%s): %w", i+1, m.String(), err)
			}
		} else {
			if err := s.movePiece(ctx, all, theState, m.S1().String(), m.S2().String(), m, nil); err != nil {
				return fmt.Errorf("playFENFile move %d (%s): %w", i+1, m.String(), err)
			}
		}

		if err := theState.game.Move(m, nil); err != nil {
			return fmt.Errorf("playFENFile apply move %d (%s): %w", i+1, m.String(), err)
		}
	}

	return s.saveGame(ctx, theState)
}
