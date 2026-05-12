package viamchess

import (
	"context"
	"testing"

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
