package viamchess

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"os"
	"strings"

	"github.com/golang/geo/r3"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
	"golang.org/x/sync/errgroup"

	"go.viam.com/rdk/components/camera"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/pointcloud"
	"go.viam.com/rdk/referenceframe"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/rimage"
	"go.viam.com/rdk/robot/framesystem"
	"go.viam.com/rdk/services/vision"
	"go.viam.com/rdk/spatialmath"
	viz "go.viam.com/rdk/vision"
	"go.viam.com/rdk/vision/classification"
	"go.viam.com/rdk/vision/objectdetection"
	"go.viam.com/rdk/vision/viscapture"
	"go.viam.com/utils/trace"

	"github.com/erh/vmodutils/touch"
)

var PieceFinderModel = family.WithModel("piece-finder")

const minPieceSize = 25.0
const squareInset = 10.0

func init() {
	resource.RegisterService(vision.API, PieceFinderModel,
		resource.Registration[vision.Service, *PieceFinderConfig]{
			Constructor: newPieceFinder,
		},
	)
}

type PieceFinderConfig struct {
	Input string // this is the cropped camera for the board, TODO: what orientation???
}

func (cfg *PieceFinderConfig) Validate(path string) ([]string, []string, error) {
	if cfg.Input == "" {
		return nil, nil, fmt.Errorf("need an input")
	}
	return []string{cfg.Input}, nil, nil
}

func newPieceFinder(ctx context.Context, deps resource.Dependencies, rawConf resource.Config, logger logging.Logger) (vision.Service, error) {
	conf, err := resource.NativeConfig[*PieceFinderConfig](rawConf)
	if err != nil {
		return nil, err
	}

	return NewPieceFinder(ctx, deps, rawConf.ResourceName(), conf, logger)
}

func NewPieceFinder(ctx context.Context, deps resource.Dependencies, name resource.Name, conf *PieceFinderConfig, logger logging.Logger) (vision.Service, error) {
	var err error

	bc := &PieceFinder{
		name:   name,
		conf:   conf,
		logger: logger,
	}

	bc.input, err = camera.FromProvider(deps, conf.Input)
	if err != nil {
		return nil, err
	}

	bc.props, err = bc.input.Properties(ctx)
	if err != nil {
		return nil, err
	}

	bc.rfs, err = framesystem.FromDependencies(deps)
	if err != nil {
		logger.Errorf("can't get framesystem: %v", err)
	}

	return bc, nil
}

type PieceFinder struct {
	resource.AlwaysRebuild
	resource.TriviallyCloseable

	name   resource.Name
	conf   *PieceFinderConfig
	logger logging.Logger

	rfs   framesystem.Service
	input camera.Camera
	props camera.Properties
}

type squareInfo struct {
	rank int
	file rune
	name string // <rank><file>

	originalBounds image.Rectangle

	color int // 0,1,2

	pc pointcloud.PointCloud
}

func scale(start, end int, amount float64) int {
	//fmt.Printf("\t %v %v %v\n", start, end, amount)
	return int(float64(end-start)*amount) + start
}

func computeSquareBounds(corners []image.Point, col, row int) image.Rectangle {

	colTopLeft := image.Point{
		scale(corners[0].X, corners[1].X, float64(col)/8),
		scale(corners[0].Y, corners[1].Y, float64(col)/8),
	}

	colTopRight := image.Point{
		scale(corners[0].X, corners[1].X, float64(1+col)/8),
		scale(corners[0].Y, corners[1].Y, float64(1+col)/8),
	}

	colBottomLeft := image.Point{
		scale(corners[3].X, corners[2].X, float64(col)/8),
		scale(corners[3].Y, corners[2].Y, float64(col)/8),
	}

	colBottomRight := image.Point{
		scale(corners[3].X, corners[2].X, float64(1+col)/8),
		scale(corners[3].Y, corners[2].Y, float64(1+col)/8),
	}

	//fmt.Printf("colTopLeft: %v\n", colTopLeft)
	//fmt.Printf("colBottomLeft: %v\n", colBottomLeft)
	//fmt.Printf("colTopRight: %v\n", colTopRight)
	//fmt.Printf("colBottomRight: %v\n", colBottomRight)

	bounds := image.Rect(
		scale(colTopLeft.X, colBottomLeft.X, float64(row)/8),
		scale(colTopLeft.Y, colBottomLeft.Y, float64(row)/8),
		scale(colTopRight.X, colBottomRight.X, float64(row+1)/8),
		scale(colTopRight.Y, colBottomRight.Y, float64(row+1)/8),
	)

	// Add inset to avoid capturing border lines between squares
	// and to account for depth/RGB alignment issues
	// Shrink by 10 pixels on each side to stay well within the square
	inset := min(squareInset, (bounds.Max.X-bounds.Min.X)/10)
	bounds.Min.X += inset
	bounds.Min.Y += inset
	bounds.Max.X -= inset
	bounds.Max.Y -= inset

	return bounds
}

func findBoardAndPieces(ctx context.Context, srcImg image.Image, pc pointcloud.PointCloud, props camera.Properties, logger logging.Logger) ([]squareInfo, error) {

	corners, err := findBoard(srcImg)
	if err != nil {
		return nil, err
	}

	logger.Debugf("corners: %v", corners)

	logger.Debugf("camera intrinsics: %#v", props.IntrinsicParams)
	if props.ExtrinsicParams != nil {
		logger.Debugf("camera extrinsics: %v %v", props.ExtrinsicParams.Translation, props.ExtrinsicParams.Orientation)
	}

	// Phase 1: pre-compute all 64 square bounds and allocate sub-clouds
	_, span := trace.StartSpan(ctx, "PieceFinder::findBoardAndPieces::ComputeSquareBounds")
	squares := make([]squareInfo, 0, 64)
	subPcs := make([]pointcloud.PointCloud, 0, 64)
	for rank := 1; rank <= 8; rank++ {
		for file := 'a'; file <= 'h'; file++ {
			name := fmt.Sprintf("%s%d", string([]byte{byte(file)}), rank)
			srcRect := computeSquareBounds(corners, int('h'-file), rank-1)
			squares = append(squares, squareInfo{
				rank:           rank,
				file:           file,
				name:           name,
				originalBounds: srcRect,
			})
			subPcs = append(subPcs, pointcloud.NewBasicEmpty())
		}
	}
	span.End()

	// Phase 2: single pass through point cloud — call PointToPixel once per point
	// instead of 64 times (once per square), reducing from O(64N) to O(N)
	_, span = trace.StartSpan(ctx, "PieceFinder::findBoardAndPieces::SinglePassPartition")
	var outerErr error
	pc.Iterate(0, 0, func(p r3.Vector, d pointcloud.Data) bool {
		x, y, err := props.PointToPixel(p)
		if err != nil {
			outerErr = err
			return false
		}
		ix, iy := int(x), int(y)
		for i, s := range squares {
			b := s.originalBounds
			if ix >= b.Min.X && ix <= b.Max.X && iy >= b.Min.Y && iy <= b.Max.Y {
				subPcs[i].Set(p, d)
				break
			}
		}
		return true
	})
	span.End()
	if outerErr != nil {
		return nil, outerErr
	}

	// Phase 3: estimate piece color for each square
	_, span = trace.StartSpan(ctx, "PieceFinder::findBoardAndPieces::EstimateColors")
	for i := range squares {
		if subPcs[i].Size() == 0 {
			span.End()
			return nil, fmt.Errorf("pc for %s is empty in findBoardAndPieces srcRect: %v", squares[i].name, squares[i].originalBounds)
		}
		squares[i].color = estimatePieceColor(subPcs[i])
		squares[i].pc = subPcs[i]
	}
	span.End()

	return squares, nil
}

// 0 - blank, 1 - white, 2 - black
func estimatePieceColor(pc pointcloud.PointCloud) int {
	minZ := pc.MetaData().MaxZ - minPieceSize
	var totalR, totalG, totalB float64
	count := 0

	pc.Iterate(0, 0, func(p r3.Vector, d pointcloud.Data) bool {
		if p.Z < minZ && d != nil && d.HasColor() {
			r, g, b := d.RGB255()
			totalR += float64(r)
			totalG += float64(g)
			totalB += float64(b)
			count++
		}
		return true
	})

	if count <= 10 {
		return 0 // blank - no piece detected
	}

	// calculate average brightness
	avgR := totalR / float64(count)
	avgG := totalG / float64(count)
	avgB := totalB / float64(count)
	brightness := (avgR + avgG + avgB) / 3.0

	// threshold to distinguish white vs black pieces
	if brightness > 128 {
		return 1 // white
	}
	return 2 // black
}

func drawString(dst *image.RGBA, x, y int, s string, c color.Color) {
	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(c),
		Face: basicfont.Face7x13,
		Dot:  fixed.Point26_6{X: fixed.I(x), Y: fixed.I(y)},
	}
	d.DrawString(s)
}

func (bc *PieceFinder) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	return nil, fmt.Errorf("DoCommand not supported")
}

func (bc *PieceFinder) Name() resource.Name {
	return bc.name
}

func (bc *PieceFinder) DetectionsFromCamera(ctx context.Context, cameraName string, extra map[string]interface{}) ([]objectdetection.Detection, error) {
	return nil, fmt.Errorf("DetectionsFromCamera not implemented")
}

func (bc *PieceFinder) Detections(ctx context.Context, img image.Image, extra map[string]interface{}) ([]objectdetection.Detection, error) {
	return nil, fmt.Errorf("Detections not implemented")
}

func (bc *PieceFinder) ClassificationsFromCamera(ctx context.Context, cameraName string, n int, extra map[string]interface{}) (classification.Classifications, error) {
	return nil, fmt.Errorf("ClassificationsFromCamera not implemented")
}

func (bc *PieceFinder) Classifications(ctx context.Context, img image.Image, n int, extra map[string]interface{}) (classification.Classifications, error) {
	return nil, fmt.Errorf("Classifications not implemented")
}

func (bc *PieceFinder) GetObjectPointClouds(ctx context.Context, cameraName string, extra map[string]interface{}) ([]*viz.Object, error) {
	ret, err := bc.CaptureAllFromCamera(ctx, cameraName, viscapture.CaptureOptions{}, extra)
	if err != nil {
		return nil, err
	}
	return ret.Objects, nil
}

func (bc *PieceFinder) CaptureAllFromCamera(ctx context.Context, cameraName string, opts viscapture.CaptureOptions, extra map[string]interface{}) (viscapture.VisCapture, error) {
	ctx, span := trace.StartSpan(ctx, "PieceFinder::CaptureAllFromCamera")
	defer span.End()

	ret := viscapture.VisCapture{}

	// Fetch image and point cloud in parallel — they are independent camera reads
	var ni []camera.NamedImage
	var pc pointcloud.PointCloud
	eg, egCtx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		_, span2 := trace.StartSpan(egCtx, "PieceFinder::CaptureAllFromCamera::Images")
		var err error
		ni, _, err = bc.input.Images(egCtx, nil, extra)
		span2.End()
		return err
	})
	eg.Go(func() error {
		_, span2 := trace.StartSpan(egCtx, "PieceFinder::CaptureAllFromCamera::NextPointCloud")
		var err error
		pc, err = bc.input.NextPointCloud(egCtx, extra)
		span2.End()
		return err
	})
	if err := eg.Wait(); err != nil {
		return ret, err
	}

	if len(ni) == 0 {
		return ret, fmt.Errorf("no images returned from input camera")
	}

	_, span2 := trace.StartSpan(ctx, "PieceFinder::CaptureAllFromCamera::Image")
	var err error
	ret.Image, err = ni[0].Image(ctx)
	span2.End()
	if err != nil {
		return ret, err
	}

	_, span2 = trace.StartSpan(ctx, "PieceFinder::CaptureAllFromCamera::findBoardAndPieces")
	squares, err := findBoardAndPieces(ctx, ret.Image, pc, bc.props, bc.logger)
	span2.End()
	if err != nil {
		if extra != nil && extra["debug"] == true {
			if err2 := rimage.SaveImage(ret.Image, "chess-debug.jpg"); err2 != nil {
				bc.logger.Errorf("failed to save debug image: %v", err2)
			}
			if corners, err2 := findBoard(ret.Image); err2 != nil {
				bc.logger.Errorf("failed to find corners for debug: %v", err2)
			} else {
				bounds := ret.Image.Bounds()
				dst := image.NewRGBA(bounds)
				draw.Draw(dst, bounds, ret.Image, image.Point{}, draw.Src)
				red := color.RGBA{255, 0, 0, 255}
				for _, corner := range corners {
					drawRect(dst, image.Rect(corner.X-5, corner.Y-5, corner.X+5, corner.Y+5), red)
				}
				if err2 = rimage.SaveImage(dst, "chess-debug-corners.jpg"); err2 != nil {
					bc.logger.Errorf("failed to save debug corners image: %v", err2)
				}
			}

			if f, err2 := os.Create("chess-debug.pcd"); err2 != nil {
				bc.logger.Errorf("failed to create debug pcd: %v", err2)
			} else {
				if err2 = pointcloud.ToPCD(pc, f, pointcloud.PCDBinary); err2 != nil {
					bc.logger.Errorf("failed to write debug pcd: %v", err2)
				}
				f.Close()
			}
			bc.logger.Warnf("findBoardAndPieces failed, saved debug data")
		}
		return ret, err
	}

	// Process all 64 squares in parallel — transforms and pickup center calculations are independent
	_, span2 = trace.StartSpan(ctx, "PieceFinder::CaptureAllFromCamera::ParallelSquareTransforms")
	ret.Objects = make([]*viz.Object, len(squares))
	ret.Detections = make([]objectdetection.Detection, len(squares)*2)

	eg2, egCtx2 := errgroup.WithContext(ctx)
	for i, s := range squares {
		i, s := i, s
		eg2.Go(func() error {
			worldPc, err := bc.rfs.TransformPointCloud(egCtx2, s.pc, bc.conf.Input, "world")
			if err != nil {
				return err
			}
			if worldPc == nil {
				return fmt.Errorf("why is pc nil")
			}

			label := fmt.Sprintf("%s-%d", s.name, s.color)
			o, err := viz.NewObjectWithLabel(worldPc, label, nil)
			if err != nil {
				return err
			}
			if o.Geometry == nil {
				return fmt.Errorf("why is Geometry nil for square: %s %v", s.name, s)
			}
			ret.Objects[i] = o
			ret.Detections[i*2] = objectdetection.NewDetectionWithoutImgBounds(s.originalBounds, 1, label)

			highPointInWorld := GetPickupCenter(o)
			highPointInCam, err := bc.rfs.TransformPose(egCtx2,
				referenceframe.NewPoseInFrame("world", spatialmath.NewPoseFromPoint(highPointInWorld)),
				bc.conf.Input,
				nil)
			if err != nil {
				return err
			}
			highPoint := highPointInCam.Pose().Point()

			highX, highY, err := bc.props.PointToPixel(r3.Vector{X: highPoint.X, Y: highPoint.Y, Z: highPoint.Z})
			if err != nil {
				return fmt.Errorf("PointToPixel failed: %w", err)
			}

			ret.Detections[i*2+1] = objectdetection.NewDetectionWithoutImgBounds(
				image.Rect(int(highX-5), int(highY-5), int(highX+5), int(highY+5)),
				1, "x-"+label)
			return nil
		})
	}
	if err := eg2.Wait(); err != nil {
		span2.End()
		return ret, err
	}
	span2.End()

	return ret, nil
}

func GetPickupCenter(o *viz.Object) r3.Vector {
	md := o.MetaData()
	center := md.Center()

	if strings.HasSuffix(o.Geometry.Label(), "-0") {
		return center
	}

	high := touch.PCFindHighestInRegion(o, image.Rect(-1000, -1000, 1000, 1000))
	return r3.Vector{
		X: (center.X + high.X) / 2,
		Y: (center.Y + high.Y) / 2,
		Z: high.Z,
	}
}

func (bc *PieceFinder) GetProperties(ctx context.Context, extra map[string]interface{}) (*vision.Properties, error) {
	return &vision.Properties{
		ObjectPCDsSupported: true,
	}, nil
}

func createDebugImage(input image.Image, squares []squareInfo) (image.Image, error) {
	// Create a copy of the input image to draw on
	bounds := input.Bounds()
	dst := image.NewRGBA(bounds)
	draw.Draw(dst, bounds, input, image.Point{}, draw.Src)

	// Draw debug info for each square
	for _, sq := range squares {
		// Draw a rectangle around the square
		drawRect(dst, sq.originalBounds, color.RGBA{0, 255, 0, 255})

		// Prepare the debug text: square name and piece color
		colorNames := []string{"", "W", "B"}
		pieceLabel := colorNames[sq.color]
		text := fmt.Sprintf("%s-%s", sq.name, pieceLabel)

		// Calculate center of the square for text placement
		centerX := (sq.originalBounds.Min.X + sq.originalBounds.Max.X) / 2
		centerY := (sq.originalBounds.Min.Y + sq.originalBounds.Max.Y) / 2

		// Adjust position to center the text (roughly)
		textX := centerX - len(text)*3
		textY := centerY + 3

		// Draw the text
		drawString(dst, textX, textY, text, color.RGBA{255, 0, 0, 255})
	}

	return dst, nil
}

// drawRect draws a rectangle outline on the image
func drawRect(img *image.RGBA, rect image.Rectangle, c color.Color) {
	// Draw top and bottom lines
	for x := rect.Min.X; x < rect.Max.X; x++ {
		if x >= 0 && x < img.Bounds().Max.X {
			if rect.Min.Y >= 0 && rect.Min.Y < img.Bounds().Max.Y {
				img.Set(x, rect.Min.Y, c)
			}
			if rect.Max.Y-1 >= 0 && rect.Max.Y-1 < img.Bounds().Max.Y {
				img.Set(x, rect.Max.Y-1, c)
			}
		}
	}
	// Draw left and right lines
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		if y >= 0 && y < img.Bounds().Max.Y {
			if rect.Min.X >= 0 && rect.Min.X < img.Bounds().Max.X {
				img.Set(rect.Min.X, y, c)
			}
			if rect.Max.X-1 >= 0 && rect.Max.X-1 < img.Bounds().Max.X {
				img.Set(rect.Max.X-1, y, c)
			}
		}
	}
}
