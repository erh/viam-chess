package viamchess

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"testing"

	"go.viam.com/rdk/rimage"
	"go.viam.com/test"
)

func TestFindBoardCorners(t *testing.T) {
	input, err := rimage.ReadImageFromFile("data/board1.jpg")
	test.That(t, err, test.ShouldBeNil)

	corners, err := findBoard(input)
	test.That(t, err, test.ShouldBeNil)
	test.That(t, len(corners), test.ShouldEqual, 4)

	t.Logf("Found corners: %v", corners)

	// Draw corners on output image
	output := image.NewRGBA(input.Bounds())
	draw.Draw(output, input.Bounds(), input, image.Point{}, draw.Src)

	// Mark each corner with a red circle
	red := color.RGBA{255, 0, 0, 255}
	for _, corner := range corners {
		drawCircle(output, corner.X, corner.Y, 10, red)
		drawCross(output, corner.X, corner.Y, 15, red)
	}

	// Save output image
	err = rimage.WriteImageToFile("data/board1_output.jpg", output)
	test.That(t, err, test.ShouldBeNil)
	t.Log("Saved output image to data/board1_output.jpg")

	// Check all 4 expected corners
	expectedCorners := []image.Point{
		{391, 50},  // top-left
		{967, 89},  // top-right
		{940, 667}, // bottom-right
		{351, 635}, // bottom-left
	}

	tolerance := 8.0
	for _, expected := range expectedCorners {
		minDist := math.MaxFloat64
		var closestCorner image.Point
		for _, corner := range corners {
			dx := float64(corner.X - expected.X)
			dy := float64(corner.Y - expected.Y)
			dist := math.Sqrt(dx*dx + dy*dy)
			if dist < minDist {
				minDist = dist
				closestCorner = corner
			}
		}
		t.Logf("Expected %v, closest found: %v, distance: %.1f pixels", expected, closestCorner, minDist)
		test.That(t, minDist, test.ShouldBeLessThan, tolerance)
	}
}

func drawCircle(img *image.RGBA, cx, cy, radius int, c color.Color) {
	for angle := 0.0; angle < 360; angle += 1 {
		x := cx + int(float64(radius)*math.Cos(angle*math.Pi/180))
		y := cy + int(float64(radius)*math.Sin(angle*math.Pi/180))
		if x >= 0 && x < img.Bounds().Max.X && y >= 0 && y < img.Bounds().Max.Y {
			img.Set(x, y, c)
		}
	}
}

func drawCross(img *image.RGBA, cx, cy, size int, c color.Color) {
	for d := -size; d <= size; d++ {
		// Horizontal line
		x := cx + d
		if x >= 0 && x < img.Bounds().Max.X && cy >= 0 && cy < img.Bounds().Max.Y {
			img.Set(x, cy, c)
		}
		// Vertical line
		y := cy + d
		if cx >= 0 && cx < img.Bounds().Max.X && y >= 0 && y < img.Bounds().Max.Y {
			img.Set(cx, y, c)
		}
	}
}

func TestFindBoardCorners2(t *testing.T) {
	input, err := rimage.ReadImageFromFile("data/board2.jpg")
	test.That(t, err, test.ShouldBeNil)

	// Debug: check initial boundary corners
	bounds := input.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	boardMask := createBoardMaskColor(input, width, height)
	boundaryPoints := findBoundary(boardMask)
	hull := convexHull(boundaryPoints)
	initialCorners := findExtremePointsSimple(hull)
	t.Logf("Initial boundary corners: %v", initialCorners)

	corners, err := findBoard(input)
	test.That(t, err, test.ShouldBeNil)
	test.That(t, len(corners), test.ShouldEqual, 4)

	t.Logf("Refined corners: %v", corners)

	// Draw corners on output image
	output := image.NewRGBA(input.Bounds())
	draw.Draw(output, input.Bounds(), input, image.Point{}, draw.Src)

	// Mark each corner with a red circle
	red := color.RGBA{255, 0, 0, 255}
	for _, corner := range corners {
		drawCircle(output, corner.X, corner.Y, 10, red)
		drawCross(output, corner.X, corner.Y, 15, red)
	}

	// Save output image
	err = rimage.WriteImageToFile("data/board2_output.jpg", output)
	test.That(t, err, test.ShouldBeNil)
	t.Log("Saved output image to data/board2_output.jpg")

	// Check all 4 expected corners
	expectedCorners := []image.Point{
		{303, 77},  // top-left
		{884, 60},  // top-right
		{906, 639}, // bottom-right
		{312, 661}, // bottom-left
	}

	tolerance := 8.0
	for _, expected := range expectedCorners {
		minDist := math.MaxFloat64
		var closestCorner image.Point
		for _, corner := range corners {
			dx := float64(corner.X - expected.X)
			dy := float64(corner.Y - expected.Y)
			dist := math.Sqrt(dx*dx + dy*dy)
			if dist < minDist {
				minDist = dist
				closestCorner = corner
			}
		}
		t.Logf("Expected %v, closest found: %v, distance: %.1f pixels", expected, closestCorner, minDist)
		test.That(t, minDist, test.ShouldBeLessThan, tolerance)
	}
}
