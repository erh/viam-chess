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

	// Check all 4 expected corners (from edge-refined line intersection)
	expectedCorners := []image.Point{
		{388, 54},  // top-left
		{965, 79},  // top-right
		{938, 664}, // bottom-right
		{359, 636}, // bottom-left
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

func TestFindBoardCorners3(t *testing.T) {
	input, err := rimage.ReadImageFromFile("data/board3.jpg")
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
	err = rimage.WriteImageToFile("data/board3_output.jpg", output)
	test.That(t, err, test.ShouldBeNil)
	t.Log("Saved output image to data/board3_output.jpg")

	// Check all 4 expected corners (from edge-refined line intersection)
	expectedCorners := []image.Point{
		{269, 7},   // top-left
		{952, 5},   // top-right
		{970, 697}, // bottom-right
		{275, 697}, // bottom-left
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

func TestFindBoardCorners2(t *testing.T) {
	input, err := rimage.ReadImageFromFile("data/board2.jpg")
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
	err = rimage.WriteImageToFile("data/board2_output.jpg", output)
	test.That(t, err, test.ShouldBeNil)
	t.Log("Saved output image to data/board2_output.jpg")

	// Check all 4 expected corners (from line intersection detection)
	expectedCorners := []image.Point{
		{302, 76},  // top-left
		{883, 56},  // top-right
		{905, 638}, // bottom-right
		{311, 654}, // bottom-left
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

func TestFindBoardCorners4(t *testing.T) {
	input, err := rimage.ReadImageFromFile("data/board4.jpg")
	test.That(t, err, test.ShouldBeNil)

	corners, err := findBoard(input)
	test.That(t, err, test.ShouldBeNil)
	test.That(t, len(corners), test.ShouldEqual, 4)

	t.Logf("Found corners: %v", corners)
	t.Logf("Image size: %dx%d", input.Bounds().Dx(), input.Bounds().Dy())

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
	err = rimage.WriteImageToFile("data/board4_output.jpg", output)
	test.That(t, err, test.ShouldBeNil)
	t.Log("Saved output image to data/board4_output.jpg")

	// Check all 4 expected corners (from edge-refined line intersection)
	expectedCorners := []image.Point{
		{269, 7},   // top-left
		{953, 5},   // top-right
		{968, 693}, // bottom-right
		{271, 697}, // bottom-left
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
