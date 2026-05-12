package viamchess

import (
	"context"
	"fmt"
	"strings"

	"github.com/corentings/chess/v2"

	"go.viam.com/rdk/vision/viscapture"
)

// Slot 0 holds a spare queen pre-placed by the human; captures fill slots 1+.
const extraQueenGraveyardSlot = 0

// Must be called BEFORE theState.game.Move(m) so the engine still sees the
// pre-move position. Promotion never places the pawn on the promotion square:
// (optional) evict capture → pawn straight to graveyard → spare queen onto promoSq.
func (s *viamChessChess) handlePromotionMove(ctx context.Context, data viscapture.VisCapture, theState *state, m *chess.Move) error {
	promoSq := m.S2().String()
	isWhite := m.S2().Rank() == chess.Rank8

	if m.HasTag(chess.Capture) {
		captured := theState.game.Position().Board().Piece(m.S2())
		if captured != chess.NoPiece {
			if err := s.movePiece(ctx, data, theState, promoSq, "-", nil, nil); err != nil {
				return fmt.Errorf("evict captured %v from %s: %w", captured, promoSq, err)
			}
			if captured.Color() == chess.White {
				theState.whiteGraveyard = append(theState.whiteGraveyard, int(captured))
			} else {
				theState.blackGraveyard = append(theState.blackGraveyard, int(captured))
			}
			// Clear the snapshot label so queen placement below doesn't re-evict.
			for _, o := range data.Objects {
				if strings.HasPrefix(o.Geometry.Label(), promoSq+"-") {
					o.Geometry.SetLabel(promoSq + "-0")
					break
				}
			}
		}
	}

	var pawnDest, queenSrc string
	var pawnPiece chess.Piece
	if isWhite {
		pawnDest = fmt.Sprintf("XW%d", len(theState.whiteGraveyard)+1)
		queenSrc = fmt.Sprintf("XW%d", extraQueenGraveyardSlot)
		pawnPiece = chess.WhitePawn
	} else {
		pawnDest = fmt.Sprintf("XB%d", len(theState.blackGraveyard)+1)
		queenSrc = fmt.Sprintf("XB%d", extraQueenGraveyardSlot)
		pawnPiece = chess.BlackPawn
	}

	if err := s.movePiece(ctx, data, theState, m.S1().String(), pawnDest, nil, nil); err != nil {
		return fmt.Errorf("move promoted pawn from %s to %s: %w", m.S1().String(), pawnDest, err)
	}
	if isWhite {
		theState.whiteGraveyard = append(theState.whiteGraveyard, int(pawnPiece))
	} else {
		theState.blackGraveyard = append(theState.blackGraveyard, int(pawnPiece))
	}

	// nil theState so the occupied-check reads the camera (engine would still
	// claim a captured piece on promoSq and auto-evict a phantom).
	if err := s.movePiece(ctx, data, nil, queenSrc, promoSq, nil, nil); err != nil {
		return fmt.Errorf("place queen on %s: %w", promoSq, err)
	}

	return nil
}
