package viamchess

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"testing"

	"go.viam.com/rdk/rimage"
	"go.viam.com/test"
)

type boardTestCase struct {
	inputFile       string
	expectedCorners []image.Point
	tolerance       float64
}

func TestFindBoardCorners(t *testing.T) {
	testCases := []boardTestCase{
		{
			inputFile: "data/board1.jpg",
			expectedCorners: []image.Point{
				{390, 48},  // top-left
				{965, 85},  // top-right
				{939, 665}, // bottom-right
				{347, 635}, // bottom-left
			},
			tolerance: 3.5, // TL/TR are ~3 pixels off due to chess pieces near top edge
		},
		{
			inputFile: "data/board2.jpg",
			expectedCorners: []image.Point{
				{305, 71},  // top-left
				{883, 59},  // top-right
				{904, 639}, // bottom-right
				{311, 660}, // bottom-left
			},
			tolerance: 4.0, // TL is ~3.6 pixels off
		},
		{
			inputFile: "data/board3.jpg",
			expectedCorners: []image.Point{
				{275, 7},   // top-left
				{952, 2},   // top-right
				{969, 683}, // bottom-right
				{271, 697}, // bottom-left
			},
			tolerance: 5.5,
		},
		{
			inputFile: "data/board4.jpg",
			expectedCorners: []image.Point{
				{275, 7},   // top-left
				{952, 2},   // top-right
				{969, 683}, // bottom-right
				{271, 697}, // bottom-left
			},
			tolerance: 3.5, // BR is ~3.2 pixels off
		},
		{
			inputFile: "data/board5.jpg",
			expectedCorners: []image.Point{
				{296, 17},  // top-left
				{970, 17},  // top-right
				{982, 700}, // bottom-right
				{283, 705}, // bottom-left
			},
			tolerance: 3.5,
		},
		{
			inputFile: "data/board6.jpg",
			expectedCorners: []image.Point{
				{306, 9},   // top-left
				{980, 10},  // top-right
				{992, 695}, // bottom-right
				{293, 699}, // bottom-left
			},
			tolerance: 3.5,
		},
		{
			inputFile: "data/board7.jpg",
			expectedCorners: []image.Point{
				{293, 17},  // top-left
				{969, 14},  // top-right
				{984, 698}, // bottom-right
				{284, 707}, // bottom-left
			},
			tolerance: 5.0,
		},
		{
			inputFile: "data/board8.jpg",
			expectedCorners: []image.Point{
				{312, 30},   // top-left
				{977, 18},   // top-right
				{1003, 693}, // bottom-right
				{313, 710},  // bottom-left
			},
			tolerance: 3.5,
		},
		{
			inputFile: "data/board9.jpg",
			expectedCorners: []image.Point{
				{312, 30},   // top-left
				{977, 18},   // top-right
				{1003, 693}, // bottom-right
				{313, 710},  // bottom-left
			},
			tolerance: 3.5,
		},
		{
			inputFile: "data/board10.jpg",
			expectedCorners: []image.Point{
				{312, 30},   // top-left
				{977, 18},   // top-right
				{1003, 693}, // bottom-right
				{313, 710},  // bottom-left
			},
			tolerance: 3.5,
		},
		{
			inputFile: "data/board11.jpg",
			expectedCorners: []image.Point{
				{333, 38},  // top-left
				{950, 42},  // top-right
				{945, 655}, // bottom-right
				{330, 652}, // bottom-left
			},
			tolerance: 4.0,
		},
		{
			inputFile: "data/board12.jpg",
			expectedCorners: []image.Point{
				{314, 22},  // top-left
				{979, 22},  // top-right
				{976, 687}, // bottom-right
				{313, 687}, // bottom-left
			},
			tolerance: 3.5,
		},
		{
			inputFile: "data/board13.jpg",
			expectedCorners: []image.Point{
				{314, 22},  // top-left
				{979, 22},  // top-right
				{976, 687}, // bottom-right
				{313, 687}, // bottom-left
			},
			tolerance: 3.5,
		},
		{
			inputFile: "data/board14.jpg",
			expectedCorners: []image.Point{
				{325, 25},   // top-left
				{1010, 15},  // top-right
				{1019, 698}, // bottom-right
				{334, 709},  // bottom-left
			},
			tolerance: 3.7,
		},
		{
			inputFile: "data/board15.jpg",
			expectedCorners: []image.Point{
				{331, 38},  // top-left
				{982, 31},  // top-right
				{985, 682}, // bottom-right
				{338, 688}, // bottom-left
			},
			tolerance: 3.5,
		},
		{
			inputFile: "data/board16.jpg",
			expectedCorners: []image.Point{
				{324, 21},   // top-left
				{1009, 15},  // top-right
				{1018, 697}, // bottom-right
				{332, 710},  // bottom-left
			},
			tolerance: 3.5,
		},
		{
			inputFile: "data/board17.jpg",
			expectedCorners: []image.Point{
				{323, 18},   // top-left
				{1009, 15},   // top-right
				{1015, 698}, // bottom-right
				{329, 708}, // bottom-left
			},
			tolerance: 3.5,
		},
		{
			inputFile: "data/board18.jpg",
			expectedCorners: []image.Point{
				{323, 18},   // top-left
				{1009, 15},   // top-right
				{1015, 698}, // bottom-right
				{329, 708}, // bottom-left
			},
			tolerance: 3.5,
		},
		{
			inputFile: "data/board19.jpg",
			expectedCorners: []image.Point{
				{324, 19},   // top-left
				{1009, 14},   // top-right
				{1016, 698}, // bottom-right
				{330, 707}, // bottom-left
			},
			tolerance: 3.5,
		},
		{
			inputFile: "data/board20.jpg",
			expectedCorners: []image.Point{
				{324, 19},   // top-left
				{1009, 14},   // top-right
				{1016, 698}, // bottom-right
				{330, 707}, // bottom-left
			},
			tolerance: 3.5,
		},
		{
			inputFile: "data/board23.jpg",
			expectedCorners: []image.Point{
				{339, 18},   // top-left
				{1029, 14},   // top-right
				{1022, 705}, // bottom-right
				{339, 698}, // bottom-left
			},
			tolerance: 3.5,
		},
		{
			inputFile: "data/board22.jpg",
			expectedCorners: []image.Point{
				{345, 14},   // top-left
				{1052, 4},   // top-right
				{1052, 712}, // bottom-right
				{354, 709}, // bottom-left
      },
      			tolerance: 3.5,
    },
    {
			inputFile: "data/board21.jpg",
			expectedCorners: []image.Point{
				{324, 18},   // top-left
				{1010, 14},   // top-right
				{1016, 698}, // bottom-right
				{330, 707}, // bottom-left
			},
			tolerance: 3.5,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.inputFile, func(t *testing.T) {
			testBoardCornerDetection(t, tc)
		})
	}
}

func testBoardCornerDetection(t *testing.T, tc boardTestCase) {
	// Read input image
	input, err := rimage.ReadImageFromFile(tc.inputFile)
	test.That(t, err, test.ShouldBeNil)

	// Find board corners
	corners, err := findBoard(input)
	test.That(t, err, test.ShouldBeNil)
	test.That(t, len(corners), test.ShouldEqual, 4)

	t.Logf("Found corners: %v", corners)
	t.Logf("Image size: %dx%d", input.Bounds().Dx(), input.Bounds().Dy())

	// Draw corners on output image
	output := image.NewRGBA(input.Bounds())
	draw.Draw(output, input.Bounds(), input, image.Point{}, draw.Src)

	// Mark detected corners with red circles
	red := color.RGBA{255, 0, 0, 255}
	for _, corner := range corners {
		drawCircle(output, corner.X, corner.Y, 10, red)
		drawCross(output, corner.X, corner.Y, 15, red)
	}

	// Mark expected corners with green circles
	green := color.RGBA{0, 255, 0, 255}
	for _, expected := range tc.expectedCorners {
		drawCircle(output, expected.X, expected.Y, 8, green)
		drawCross(output, expected.X, expected.Y, 12, green)
	}

	// Save output image
	// Extract base name from input file (e.g., "data/board1.jpg" -> "board1")
	baseName := tc.inputFile[5 : len(tc.inputFile)-4]
	outputFile := fmt.Sprintf("data/%s_output.jpg", baseName)
	err = rimage.WriteImageToFile(outputFile, output)
	test.That(t, err, test.ShouldBeNil)
	t.Logf("Saved output image to %s", outputFile)

	// Verify corners match expected values within tolerance
	for _, expected := range tc.expectedCorners {
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
		test.That(t, minDist, test.ShouldBeLessThan, tc.tolerance)
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
