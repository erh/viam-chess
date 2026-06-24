package viamchess

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/corentings/chess/v2"
	"go.viam.com/test"
)

func TestReset1(t *testing.T) {
	ctx := context.Background()

	theMainState, err := readState(ctx, "data/reset1.json")
	test.That(t, err, test.ShouldBeNil)

	theState := &resetState{theMainState.game.Position().Board(), theMainState.whiteGraveyard, theMainState.blackGraveyard}

	from, to, err := nextResetMove(theState)
	test.That(t, err, test.ShouldBeNil)
	test.That(t, from.String(), test.ShouldEqual, "e4")
	test.That(t, to.String(), test.ShouldEqual, "e2")
}

func TestReset2(t *testing.T) {
	ctx := context.Background()

	theMainState, err := readState(ctx, "data/reset2.json")
	test.That(t, err, test.ShouldBeNil)

	theState := &resetState{theMainState.game.Position().Board(), theMainState.whiteGraveyard, theMainState.blackGraveyard}

	// -

	from, to, err := nextResetMove(theState)
	test.That(t, err, test.ShouldBeNil)
	test.That(t, from.String(), test.ShouldEqual, "f3")
	test.That(t, to.String(), test.ShouldEqual, "g1")

	err = theState.applyMove(from, to)
	test.That(t, err, test.ShouldBeNil)

	// -

	from, to, err = nextResetMove(theState)
	test.That(t, err, test.ShouldBeNil)
	test.That(t, from.String(), test.ShouldEqual, "e4")
	test.That(t, to.String(), test.ShouldEqual, "d2")

	err = theState.applyMove(from, to)
	test.That(t, err, test.ShouldBeNil)

	// -

	from, to, err = nextResetMove(theState)
	test.That(t, err, test.ShouldBeNil)
	test.That(t, squareToString(from), test.ShouldEqual, "XW1")
	test.That(t, to.String(), test.ShouldEqual, "e2")

	err = theState.applyMove(from, to)
	test.That(t, err, test.ShouldBeNil)

	// -

	from, to, err = nextResetMove(theState)
	test.That(t, err, test.ShouldBeNil)
	test.That(t, squareToString(from), test.ShouldEqual, "c6")
	test.That(t, to.String(), test.ShouldEqual, "b8")

	err = theState.applyMove(from, to)
	test.That(t, err, test.ShouldBeNil)

	// -

	from, to, err = nextResetMove(theState)
	test.That(t, err, test.ShouldBeNil)
	test.That(t, squareToString(from), test.ShouldEqual, "d4")
	test.That(t, to.String(), test.ShouldEqual, "e7")

	err = theState.applyMove(from, to)
	test.That(t, err, test.ShouldBeNil)

	// -

	from, to, err = nextResetMove(theState)
	test.That(t, err, test.ShouldBeNil)
	test.That(t, from, test.ShouldEqual, -1)
	test.That(t, to, test.ShouldEqual, -1)

}

// TestResetCheckpointResume verifies that writing the current resetState as a
// FEN-only savedState and reading it back yields the same nextResetMove
// sequence as an uninterrupted run.
func TestResetCheckpointResume(t *testing.T) {
	ctx := context.Background()

	// Uninterrupted baseline: collect the full move sequence from reset2.json.
	baseline, err := readState(ctx, "data/reset2.json")
	test.That(t, err, test.ShouldBeNil)
	baseState := &resetState{baseline.game.Position().Board(), baseline.whiteGraveyard, baseline.blackGraveyard}

	type move struct{ from, to chess.Square }
	var allMoves []move
	for {
		from, to, err := nextResetMove(baseState)
		test.That(t, err, test.ShouldBeNil)
		if from < 0 {
			break
		}
		allMoves = append(allMoves, move{from, to})
		test.That(t, baseState.applyMove(from, to), test.ShouldBeNil)
	}
	test.That(t, len(allMoves), test.ShouldBeGreaterThan, 2)

	// Now redo it: run the first two moves, write a checkpoint to disk, reload,
	// and confirm the remaining moves match the baseline.
	resumed, err := readState(ctx, "data/reset2.json")
	test.That(t, err, test.ShouldBeNil)
	rs := &resetState{resumed.game.Position().Board(), resumed.whiteGraveyard, resumed.blackGraveyard}
	for i := 0; i < 2; i++ {
		from, to, err := nextResetMove(rs)
		test.That(t, err, test.ShouldBeNil)
		test.That(t, rs.applyMove(from, to), test.ShouldBeNil)
	}

	checkpoint := savedState{
		FEN:            rs.board.String() + " w KQkq - 0 1",
		WhiteGraveyard: rs.whiteGraveyard,
		BlackGraveyard: rs.blackGraveyard,
	}
	checkpointPath := filepath.Join(t.TempDir(), "state.json")
	b, err := json.MarshalIndent(&checkpoint, "", "  ")
	test.That(t, err, test.ShouldBeNil)
	test.That(t, os.WriteFile(checkpointPath, b, 0666), test.ShouldBeNil)

	reloaded, err := readState(ctx, checkpointPath)
	test.That(t, err, test.ShouldBeNil)
	reloadedRS := &resetState{reloaded.game.Position().Board(), reloaded.whiteGraveyard, reloaded.blackGraveyard}

	for i := 2; i < len(allMoves); i++ {
		from, to, err := nextResetMove(reloadedRS)
		test.That(t, err, test.ShouldBeNil)
		test.That(t, from, test.ShouldEqual, allMoves[i].from)
		test.That(t, to, test.ShouldEqual, allMoves[i].to)
		test.That(t, reloadedRS.applyMove(from, to), test.ShouldBeNil)
	}

	from, to, err := nextResetMove(reloadedRS)
	test.That(t, err, test.ShouldBeNil)
	test.That(t, from, test.ShouldEqual, -1)
	test.That(t, to, test.ShouldEqual, -1)
}
