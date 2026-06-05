package viamchess

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"os"
	"regexp"
	"testing"

	"github.com/golang/geo/r3"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/pointcloud"
	"go.viam.com/rdk/rimage"
	"go.viam.com/rdk/vision/objectdetection"
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

	res := computeSquareBounds(corners, 0, 0, defaultClassifyConfig().SquareInset)
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

	res = computeSquareBounds(corners, 0, 0, defaultClassifyConfig().SquareInset)
	test.That(t, res.Min.X, test.ShouldEqual, 366)
	test.That(t, res.Min.Y, test.ShouldEqual, 9)

	res = computeSquareBounds(corners, 0, 6, defaultClassifyConfig().SquareInset)
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

	squares, err := findBoardAndPieces(context.Background(), input, pc, touch.RealSensePropertiesD435At1280by720, logger, defaultClassifyConfig())
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

	squares, err := findBoardAndPieces(context.Background(), input, pc, touch.RealSensePropertiesD435At1280by720, logger, defaultClassifyConfig())
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

// TestBoardPiecesLayout auto-discovers data/board<N>_pieces.txt files written by
// the `capture-board-pieces` CLI command, runs the piece detector against each
// board's image and pointcloud, and asserts the detected color of every square
// matches the saved layout.
func TestBoardPiecesLayout(t *testing.T) {
	entries, err := os.ReadDir("data")
	test.That(t, err, test.ShouldBeNil)

	re := regexp.MustCompile(`^board(\d+)_pieces\.txt$`)
	var boards []string
	for _, e := range entries {
		m := re.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		boards = append(boards, "board"+m[1])
	}
	if len(boards) == 0 {
		t.Skip("no board*_pieces.txt files in data/")
	}

	for _, boardName := range boards {
		t.Run(boardName, func(t *testing.T) {
			verifyPiecesLayout(t, boardName)
		})
	}
}

func verifyPiecesLayout(t *testing.T, boardName string) {
	logger := logging.NewTestLogger(t)

	input, err := rimage.ReadImageFromFile("data/" + boardName + ".jpg")
	test.That(t, err, test.ShouldBeNil)

	pc, err := pointcloud.NewFromFile("data/"+boardName+".pcd", "")
	test.That(t, err, test.ShouldBeNil)

	expected, err := ReadPiecesLayout("data/" + boardName + "_pieces.txt")
	test.That(t, err, test.ShouldBeNil)

	// Use the camera properties saved alongside the data — piece classification
	// is sensitive to intrinsics/extrinsics via Properties.PointToPixel, so
	// reusing the same props is what makes the test reproduce the CLI's output.
	props, err := ReadCameraProperties("data/" + boardName + "_props.json")
	test.That(t, err, test.ShouldBeNil)

	actual, err := DetectBoardPieces(context.Background(), input, pc, props, logger)
	test.That(t, err, test.ShouldBeNil)

	for rank := 1; rank <= 8; rank++ {
		for file := 'a'; file <= 'h'; file++ {
			name := fmt.Sprintf("%c%d", file, rank)
			if actual[name] != expected[name] {
				t.Errorf("%s: expected %v, got %v", name, expected[name], actual[name])
			}
		}
	}
}

// TestPcDiagnose3DImgMeansUseInImgDenominator regression-tests a bug where
// TopMeanImgR/G/B and TopColorDivergence were computed by summing only the top
// points whose projected pixel landed inside the source image, but dividing by
// the count of *all* colored top points. That silently underestimated the
// divergence whenever any top point projected out-of-image, which could lower
// real divergence below the rejection guard and cause classifyPieceColor to
// trust an unreliable 3D verdict.
func TestPcDiagnose3DImgMeansUseInImgDenominator(t *testing.T) {
	// 1280x720 green image. We'll place top points whose attached color is
	// red, so the per-point img-vs-attached divergence is the same constant
	// at every in-img sample: (|255-0| + |0-100| + |0-0|) / 3 = 118.33.
	imgRect := image.Rect(0, 0, 1280, 720)
	rgba := image.NewRGBA(imgRect)
	for y := 0; y < 720; y++ {
		for x := 0; x < 1280; x++ {
			rgba.Set(x, y, color.NRGBA{R: 0, G: 100, B: 0, A: 255})
		}
	}

	props := touch.RealSensePropertiesD435At1280by720

	// 3D points. Two top points project inside the image, one projects outside
	// (x > 1280). All carry the same red color. With the bug, the out-of-image
	// point dilutes the divergence average; with the fix, it's excluded from
	// the denominator and the divergence equals the per-point constant.
	pc := pointcloud.NewBasicEmpty()
	red := color.NRGBA{R: 255, G: 0, B: 0, A: 255}

	// Board-level filler so boardPlaneZ lands at z=1000 and minZCutoff = 975.
	// pcDiagnose3D treats z < minZCutoff as top.
	const boardZ = 1000.0
	for i := 0; i < 20; i++ {
		err := pc.Set(r3.Vector{X: float64(i), Y: 0, Z: boardZ}, pointcloud.NewColoredData(color.NRGBA{R: 50, G: 50, B: 50, A: 255}))
		test.That(t, err, test.ShouldBeNil)
	}

	// Top points at z=900 (well below the cutoff). To project to a pixel (u, v)
	// with this intrinsic at depth Z: X = (u-ppx)*Z/fx, Y = (v-ppy)*Z/fy.
	intr := props.IntrinsicParams
	topAt := func(u, v, z float64) r3.Vector {
		return r3.Vector{
			X: (u - intr.Ppx) * z / intr.Fx,
			Y: (v - intr.Ppy) * z / intr.Fy,
			Z: z,
		}
	}

	// Two in-image top points (pixels 200,200 and 800,500), one out-of-image
	// top point (pixel 1500,200 — x past the 1280 image width).
	for _, p := range []r3.Vector{
		topAt(200, 200, 900),
		topAt(800, 500, 900),
		topAt(1500, 200, 900),
	} {
		err := pc.Set(p, pointcloud.NewColoredData(red))
		test.That(t, err, test.ShouldBeNil)
	}

	d := pcDiagnose3D(pc, rgba, props, 0, defaultClassifyConfig().MinPieceSize)

	// All 3 top points are colored; their attached-color means use that
	// denominator. Img-side means and divergence use the in-image subset only.
	test.That(t, d.TopColoredCount, test.ShouldEqual, 3)

	const expectedDivergence = (255.0 + 100.0 + 0.0) / 3.0
	test.That(t, d.TopColorDivergence, test.ShouldAlmostEqual, expectedDivergence, 0.01)

	// Image is solid green (0, 100, 0) — averaging over the two in-img top
	// points gives those values, not the diluted (0, 100*2/3, 0) the bug
	// produced.
	test.That(t, d.TopMeanImgR, test.ShouldAlmostEqual, 0.0, 0.01)
	test.That(t, d.TopMeanImgG, test.ShouldAlmostEqual, 100.0, 0.01)
	test.That(t, d.TopMeanImgB, test.ShouldAlmostEqual, 0.0, 0.01)

	// Attached-color means use all three top points but the color is constant.
	test.That(t, d.TopMeanAttachedR, test.ShouldAlmostEqual, 255.0, 0.01)
	test.That(t, d.TopMeanAttachedG, test.ShouldAlmostEqual, 0.0, 0.01)
	test.That(t, d.TopMeanAttachedB, test.ShouldAlmostEqual, 0.0, 0.01)
}

func TestMLLabelToColor(t *testing.T) {
	test.That(t, mlLabelToColor("white"), test.ShouldEqual, 1)
	test.That(t, mlLabelToColor("White-Pawn"), test.ShouldEqual, 1)
	test.That(t, mlLabelToColor("white-knight"), test.ShouldEqual, 1)
	test.That(t, mlLabelToColor("black"), test.ShouldEqual, 2)
	test.That(t, mlLabelToColor("black-king"), test.ShouldEqual, 2)
	test.That(t, mlLabelToColor("BLACK-QUEEN"), test.ShouldEqual, 2)
	test.That(t, mlLabelToColor(""), test.ShouldEqual, 0)
	test.That(t, mlLabelToColor("other"), test.ShouldEqual, 0)
	test.That(t, mlLabelToColor("piece"), test.ShouldEqual, 0)
}

// imgBounds for objectdetection.NewDetection — not actually used by Overlaps,
// but the constructor requires it. Make it big enough to contain every test bbox.
var mlTestImgBounds = image.Rect(0, 0, 10000, 10000)

func mlDet(box image.Rectangle, score float64, label string) objectdetection.Detection {
	return objectdetection.NewDetection(mlTestImgBounds, box, score, label)
}

func TestPickOverlappingDetection(t *testing.T) {
	rect := image.Rect(100, 100, 200, 200)

	// Empty list.
	test.That(t, pickOverlappingDetection(rect, nil), test.ShouldBeNil)

	// No overlap.
	none := []objectdetection.Detection{
		mlDet(image.Rect(0, 0, 50, 50), 0.9, "white-pawn"),
		mlDet(image.Rect(300, 300, 400, 400), 0.95, "black-king"),
	}
	test.That(t, pickOverlappingDetection(rect, none), test.ShouldBeNil)

	// One overlap.
	one := []objectdetection.Detection{
		mlDet(image.Rect(0, 0, 50, 50), 0.9, "white-pawn"),
		mlDet(image.Rect(150, 150, 250, 250), 0.7, "black-pawn"),
	}
	got := pickOverlappingDetection(rect, one)
	test.That(t, got, test.ShouldNotBeNil)
	test.That(t, got.Label(), test.ShouldEqual, "black-pawn")

	// Multiple overlaps — highest confidence wins.
	multi := []objectdetection.Detection{
		mlDet(image.Rect(150, 150, 250, 250), 0.6, "black-pawn"),
		mlDet(image.Rect(110, 110, 190, 190), 0.95, "white-knight"),
		mlDet(image.Rect(180, 180, 220, 220), 0.5, "black-bishop"),
	}
	got = pickOverlappingDetection(rect, multi)
	test.That(t, got, test.ShouldNotBeNil)
	test.That(t, got.Label(), test.ShouldEqual, "white-knight")
}

func TestMergeMLColors(t *testing.T) {
	logger := logging.NewTestLogger(t)

	// Squares laid out in a simple grid; their coordinate space is the
	// "cropped" image. ML detections will be in the "full" image space,
	// offset by cropOrigin.
	cropOrigin := image.Point{X: 1000, Y: 500}
	squares := []squareInfo{
		// 0: piece-finder says white (1), ML overlap says black → override to 2.
		{name: "a1", originalBounds: image.Rect(0, 0, 100, 100), color: 1},
		// 1: piece-finder says black (2), ML overlap says white → override to 1.
		{name: "a2", originalBounds: image.Rect(100, 0, 200, 100), color: 2},
		// 2: piece-finder says white (1), NO ML overlap → keep 1.
		{name: "a3", originalBounds: image.Rect(200, 0, 300, 100), color: 1},
		// 3: piece-finder says EMPTY (0), ML has an overlapping detection → MUST stay 0.
		{name: "a4", originalBounds: image.Rect(300, 0, 400, 100), color: 0},
		// 4: piece-finder says white (1), overlapping ML has unrecognized label → keep 1.
		{name: "a5", originalBounds: image.Rect(400, 0, 500, 100), color: 1},
		// 5: piece-finder says black (2), two overlapping ML dets at different scores → higher wins.
		{name: "a6", originalBounds: image.Rect(500, 0, 600, 100), color: 2},
	}

	// All detection bboxes are translated into FULL-image coords by adding cropOrigin.
	dets := []objectdetection.Detection{
		// Overlaps a1 (full coords 1000,500 → 1100,600). Says black.
		mlDet(image.Rect(1050, 550, 1080, 590), 0.9, "black-rook"),
		// Overlaps a2. Says white.
		mlDet(image.Rect(1100, 500, 1200, 600), 0.85, "white-bishop"),
		// Note: a3 has no detection.
		// Overlaps a4 (empty square). Should be ignored because color==0.
		mlDet(image.Rect(1310, 510, 1390, 590), 0.95, "white-queen"),
		// Overlaps a5 with unrecognized label.
		mlDet(image.Rect(1420, 520, 1480, 580), 0.99, "unknown-label"),
		// Two overlaps on a6 — higher-score "white" wins.
		mlDet(image.Rect(1510, 510, 1590, 590), 0.4, "black-pawn"),
		mlDet(image.Rect(1520, 520, 1580, 580), 0.92, "white-king"),
	}

	mergeMLColors(squares, dets, cropOrigin, logger)

	test.That(t, squares[0].color, test.ShouldEqual, 2) // flipped 1 -> 2
	test.That(t, squares[1].color, test.ShouldEqual, 1) // flipped 2 -> 1
	test.That(t, squares[2].color, test.ShouldEqual, 1) // no det, kept
	test.That(t, squares[3].color, test.ShouldEqual, 0) // was empty, must stay empty
	test.That(t, squares[4].color, test.ShouldEqual, 1) // unrecognized label, kept
	test.That(t, squares[5].color, test.ShouldEqual, 1) // higher-score white wins
}

func TestMergeMLColorsNoOpWhenNoCropOrigin(t *testing.T) {
	logger := logging.NewTestLogger(t)
	squares := []squareInfo{
		{name: "e4", originalBounds: image.Rect(400, 400, 500, 500), color: 1},
	}
	dets := []objectdetection.Detection{
		mlDet(image.Rect(420, 420, 480, 480), 0.9, "black-knight"),
	}
	mergeMLColors(squares, dets, image.Point{}, logger)
	test.That(t, squares[0].color, test.ShouldEqual, 2)
}
