package viamchess

import (
	"context"
	"image"
	"os"
	"testing"

	"github.com/golang/geo/r3"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/pointcloud"
	"go.viam.com/rdk/rimage"
	"go.viam.com/test"

	"github.com/erh/vmodutils/touch"
)

func TestScale(t *testing.T) {
	test.That(t, scale(0, 10, .5), test.ShouldEqual, 5)
	test.That(t, scale(5, 15, .5), test.ShouldEqual, 10)

}

func TestComputeSquareBounds(t *testing.T) {

	corners := []image.Point{
		{0, 0},
		{80, 0},
		{80, 80},
		{0, 80},
	}

	res := computeSquareBounds(corners, 0, 0)
	test.That(t, res.Min.X, test.ShouldEqual, 1)
	test.That(t, res.Min.Y, test.ShouldEqual, 1)

	test.That(t, res.Max.X, test.ShouldEqual, 9)
	test.That(t, res.Max.Y, test.ShouldEqual, 9)

	corners = []image.Point{
		{360, 3},
		{940, 5},
		{1011, 688},
		{257, 680},
	}

	res = computeSquareBounds(corners, 0, 0)
	test.That(t, res.Min.X, test.ShouldEqual, 366)
	test.That(t, res.Min.Y, test.ShouldEqual, 9)

	res = computeSquareBounds(corners, 0, 6)
	test.That(t, res.Min.X, test.ShouldEqual, 290)
	test.That(t, res.Min.Y, test.ShouldEqual, 517)

}

func testBoardPiece(t *testing.T, boardName string) {
	logger := logging.NewTestLogger(t)
	// Read the input image
	imageFile := "data/" + boardName + ".jpg"
	input, err := rimage.ReadImageFromFile(imageFile)
	test.That(t, err, test.ShouldBeNil)

	// Read the pointcloud
	pcdFile := "data/" + boardName + ".pcd"
	pc, err := pointcloud.NewFromFile(pcdFile, "")
	test.That(t, err, test.ShouldBeNil)

	squares, err := findBoardAndPieces(context.Background(), input, pc, touch.RealSensePropertiesD435At1280by720, logger)
	test.That(t, err, test.ShouldBeNil)

	// Create debug image with square labels
	out, err := createDebugImage(input, squares)
	test.That(t, err, test.ShouldBeNil)

	// Save the output image for inspection
	outputFile := "data/" + boardName + "_piece_test_output.jpg"
	err = rimage.WriteImageToFile(outputFile, out)
	test.That(t, err, test.ShouldBeNil)
	t.Logf("Saved output image to %s", outputFile)

	// Verify we have 64 squares
	test.That(t, len(squares), test.ShouldEqual, 64)

	// Verify every square has a valid (non-empty) pointcloud
	emptySquares := []string{}
	for _, sq := range squares {
		if sq.pc == nil || sq.pc.Size() == 0 {
			emptySquares = append(emptySquares, sq.name)
		}
	}

	if len(emptySquares) > 0 {
		t.Errorf("Found %d squares with empty pointclouds: %v", len(emptySquares), emptySquares)
	}

	// Log square info for debugging
	for _, sq := range squares {
		pcSize := 0
		if sq.pc != nil {
			pcSize = sq.pc.Size()
		}
		t.Logf("Square %s: color=%d, pc_size=%d", sq.name, sq.color, pcSize)
	}
}

func TestBoardPiece4(t *testing.T) {
	testBoardPiece(t, "board4")
}

func TestBoardPiece13(t *testing.T) {
	testBoardPiece(t, "board13")
}

func TestBoard13E2Pointcloud(t *testing.T) {
	logger := logging.NewTestLogger(t)
	// Read the input image
	input, err := rimage.ReadImageFromFile("data/board13.jpg")
	test.That(t, err, test.ShouldBeNil)

	// Read the pointcloud
	pc, err := pointcloud.NewFromFile("data/board13.pcd", "")
	test.That(t, err, test.ShouldBeNil)

	squares, err := findBoardAndPieces(context.Background(), input, pc, touch.RealSensePropertiesD435At1280by720, logger)
	test.That(t, err, test.ShouldBeNil)

	// Find the e2 square
	var e2Square *squareInfo
	for i := range squares {
		if squares[i].name == "e2" {
			e2Square = &squares[i]
			break
		}
	}

	test.That(t, e2Square, test.ShouldNotBeNil)
	test.That(t, e2Square.pc, test.ShouldNotBeNil)

	t.Logf("e2 square originalBounds: %v", e2Square.originalBounds)
	t.Logf("e2 square color: %d", e2Square.color)
	t.Logf("e2 pointcloud size: %d", e2Square.pc.Size())

	// Log some points from the pointcloud
	t.Log("Sample points from e2 pointcloud:")
	count := 0
	maxPoints := 10
	e2Square.pc.Iterate(0, 0, func(p r3.Vector, d pointcloud.Data) bool {
		if count >= maxPoints {
			return false
		}
		if d != nil && d.HasColor() {
			r, g, b := d.RGB255()
			t.Logf("  Point %d: x=%.3f, y=%.3f, z=%.3f, rgb=(%d,%d,%d)", count, p.X, p.Y, p.Z, r, g, b)
		} else {
			t.Logf("  Point %d: x=%.3f, y=%.3f, z=%.3f (no color)", count, p.X, p.Y, p.Z)
		}
		count++
		return true
	})

	// Save complete e2 pointcloud to PCD file
	f, err := os.Create("data/board13_e2.pcd")
	test.That(t, err, test.ShouldBeNil)
	defer f.Close()

	err = pointcloud.ToPCD(e2Square.pc, f, pointcloud.PCDBinary)
	test.That(t, err, test.ShouldBeNil)

	t.Log("Saved complete e2 pointcloud to data/board13_e2.pcd")
}
