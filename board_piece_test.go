package viamchess

import (
	"testing"

	"go.viam.com/rdk/pointcloud"
	"go.viam.com/rdk/rimage"
	"go.viam.com/test"

	"github.com/erh/vmodutils/touch"
)

func TestBoardPiece4(t *testing.T) {
	// Read the input image
	input, err := rimage.ReadImageFromFile("data/board4.jpg")
	test.That(t, err, test.ShouldBeNil)

	// Read the pointcloud
	pc, err := pointcloud.NewFromFile("data/board4.pcd", "")
	test.That(t, err, test.ShouldBeNil)

	// Find the board corners (same as camera does)
	corners, err := findBoard(input)
	test.That(t, err, test.ShouldBeNil)
	test.That(t, len(corners), test.ShouldEqual, 4)
	t.Logf("Found corners: %v", corners)

	// Perform perspective transform on the image (same as camera does)
	outputSize := 800
	transformedImg := perspectiveTransform(input, corners, outputSize)

	// Filter and transform the pointcloud so coordinates match the transformed image
	props := touch.RealSenseProperties
	filteredPC, err := filterAndTransformPointCloud(pc, corners, outputSize, props)
	test.That(t, err, test.ShouldBeNil)
	t.Logf("Original PC size: %d, Filtered PC size: %d", pc.Size(), filteredPC.Size())

	// Call BoardDebugImageHack with the transformed image and filtered pointcloud
	out, squares, err := BoardDebugImageHack(transformedImg, filteredPC, props)
	test.That(t, err, test.ShouldBeNil)

	// Save the output image for inspection
	err = rimage.WriteImageToFile("data/board4_piece_test_output.jpg", out)
	test.That(t, err, test.ShouldBeNil)
	t.Log("Saved output image to data/board4_piece_test_output.jpg")

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
