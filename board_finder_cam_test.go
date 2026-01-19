package viamchess

import (
	"image"
	"testing"

	"go.viam.com/rdk/rimage"
	"go.viam.com/test"
)

func TestBoardFinderCamTransform(t *testing.T) {
	// Load test image
	input, err := rimage.ReadImageFromFile("data/board1.jpg")
	test.That(t, err, test.ShouldBeNil)

	// Find corners
	corners, err := findBoard(input)
	test.That(t, err, test.ShouldBeNil)
	test.That(t, len(corners), test.ShouldEqual, 4)

	t.Logf("Corners: %v", corners)

	// Apply perspective transform
	outputSize := 800
	output := perspectiveTransform(input, corners, outputSize)

	// Verify output dimensions
	test.That(t, output.Bounds().Dx(), test.ShouldEqual, outputSize)
	test.That(t, output.Bounds().Dy(), test.ShouldEqual, outputSize)

	// Save output for visual inspection
	err = rimage.WriteImageToFile("data/board1_transformed.jpg", output)
	test.That(t, err, test.ShouldBeNil)
	t.Log("Saved transformed image to data/board1_transformed.jpg")

	// The output should be a square image with the chess board filling it
	// Each square should be approximately outputSize/8 = 100 pixels
	squareSize := outputSize / 8
	t.Logf("Expected square size: %d pixels", squareSize)
}

func TestBoardFinderCamTransform2(t *testing.T) {
	// Load test image
	input, err := rimage.ReadImageFromFile("data/board2.jpg")
	test.That(t, err, test.ShouldBeNil)

	// Find corners
	corners, err := findBoard(input)
	test.That(t, err, test.ShouldBeNil)
	test.That(t, len(corners), test.ShouldEqual, 4)

	t.Logf("Corners: %v", corners)

	// Apply perspective transform
	outputSize := 800
	output := perspectiveTransform(input, corners, outputSize)

	// Verify output dimensions
	test.That(t, output.Bounds().Dx(), test.ShouldEqual, outputSize)
	test.That(t, output.Bounds().Dy(), test.ShouldEqual, outputSize)

	// Save output for visual inspection
	err = rimage.WriteImageToFile("data/board2_transformed.jpg", output)
	test.That(t, err, test.ShouldBeNil)
	t.Log("Saved transformed image to data/board2_transformed.jpg")
}

func TestBoardFinderCamTransform3(t *testing.T) {
	// Load test image
	input, err := rimage.ReadImageFromFile("data/board3.jpg")
	test.That(t, err, test.ShouldBeNil)

	// Find corners
	corners, err := findBoard(input)
	test.That(t, err, test.ShouldBeNil)
	test.That(t, len(corners), test.ShouldEqual, 4)

	t.Logf("Corners: %v", corners)

	// Apply perspective transform
	outputSize := 800
	output := perspectiveTransform(input, corners, outputSize)

	// Verify output dimensions
	test.That(t, output.Bounds().Dx(), test.ShouldEqual, outputSize)
	test.That(t, output.Bounds().Dy(), test.ShouldEqual, outputSize)

	// Save output for visual inspection
	err = rimage.WriteImageToFile("data/board3_transformed.jpg", output)
	test.That(t, err, test.ShouldBeNil)
	t.Log("Saved transformed image to data/board3_transformed.jpg")
}

func TestBoardFinderCamTransform4(t *testing.T) {
	// Load test image
	input, err := rimage.ReadImageFromFile("data/board4.jpg")
	test.That(t, err, test.ShouldBeNil)

	// Find corners
	corners, err := findBoard(input)
	test.That(t, err, test.ShouldBeNil)
	test.That(t, len(corners), test.ShouldEqual, 4)

	t.Logf("Corners: %v", corners)

	// Apply perspective transform
	outputSize := 800
	output := perspectiveTransform(input, corners, outputSize)

	// Verify output dimensions
	test.That(t, output.Bounds().Dx(), test.ShouldEqual, outputSize)
	test.That(t, output.Bounds().Dy(), test.ShouldEqual, outputSize)

	// Save output for visual inspection
	err = rimage.WriteImageToFile("data/board4_transformed.jpg", output)
	test.That(t, err, test.ShouldBeNil)
	t.Log("Saved transformed image to data/board4_transformed.jpg")
}

func TestPerspectiveTransformBasic(t *testing.T) {
	// Create a simple test image with known content
	src := image.NewRGBA(image.Rect(0, 0, 100, 100))

	// Fill with a gradient pattern
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			src.Set(x, y, image.White)
		}
	}

	// Draw a border
	for i := 0; i < 100; i++ {
		src.Set(i, 0, image.Black)
		src.Set(i, 99, image.Black)
		src.Set(0, i, image.Black)
		src.Set(99, i, image.Black)
	}

	// Test with identity-like corners (no rotation, just scaling)
	corners := []image.Point{
		{0, 0},
		{99, 0},
		{99, 99},
		{0, 99},
	}

	outputSize := 50
	output := perspectiveTransform(src, corners, outputSize)

	test.That(t, output.Bounds().Dx(), test.ShouldEqual, outputSize)
	test.That(t, output.Bounds().Dy(), test.ShouldEqual, outputSize)
}
