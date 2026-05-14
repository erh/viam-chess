package viamchess

import (
	"context"
	"testing"
	"time"

	"go.viam.com/rdk/logging"
	"go.viam.com/test"
)

// TestSkillCommand verifies the Skill DoCommand path: sending {skill: N}
// updates s.skillAdjust, and a subsequent board-snapshot reflects that value.
func TestSkillCommand(t *testing.T) {
	ctx := context.Background()
	logger := logging.NewTestLogger(t)

	s := &viamChessChess{
		logger:      logger,
		skillAdjust: 50, // initial neutral value
	}
	// Pre-populate the board cache so board-snapshot's fast path returns.
	// We don't care about most of the snapshot contents — just that `skill`
	// surfaces correctly alongside the other fields.
	s.boardCache.ready = true
	s.boardCache.fen = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"
	s.boardCache.cameraBoard = map[string]interface{}{}
	s.boardCache.whiteGraveyard = []interface{}{}
	s.boardCache.blackGraveyard = []interface{}{}
	s.boardCache.capturedAt = time.Now()
	s.boardCache.gameEvents = GameEventsResult{
		Event: "none", Outcome: "in_progress", Method: "none", Turn: "white",
	}

	// 1. Initial snapshot reports the seeded skillAdjust.
	res, err := s.DoCommand(ctx, map[string]interface{}{"board-snapshot": true})
	test.That(t, err, test.ShouldBeNil)
	test.That(t, res["skill"], test.ShouldEqual, 50.0)

	// 2. Setting skill updates the internal value.
	_, err = s.DoCommand(ctx, map[string]interface{}{"skill": 75.0})
	test.That(t, err, test.ShouldBeNil)
	test.That(t, s.skillAdjust, test.ShouldEqual, 75.0)

	// 3. Next snapshot returns the new value.
	res, err = s.DoCommand(ctx, map[string]interface{}{"board-snapshot": true})
	test.That(t, err, test.ShouldBeNil)
	test.That(t, res["skill"], test.ShouldEqual, 75.0)
}
