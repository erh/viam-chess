package viamchess

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"

	"github.com/golang/geo/r3"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"go.viam.com/rdk/components/camera"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/pointcloud"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/rimage"
	"go.viam.com/rdk/robot/framesystem"
	"go.viam.com/rdk/services/vision"
	viz "go.viam.com/rdk/vision"
	"go.viam.com/rdk/vision/classification"
	"go.viam.com/rdk/vision/objectdetection"
	"go.viam.com/rdk/vision/viscapture"
	"go.viam.com/utils/trace"

	"github.com/erh/vmodutils/touch"
)

var PieceFinderModel = family.WithModel("piece-finder")

const minPieceSize = 25.0

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

func BoardDebugImageHack(srcImg image.Image, pc pointcloud.PointCloud, props camera.Properties) (image.Image, []squareInfo, error) {
	dst := image.NewRGBA(image.Rect(0, 0, srcImg.Bounds().Max.Y, srcImg.Bounds().Max.Y))

	xOffset := (srcImg.Bounds().Max.X - srcImg.Bounds().Max.Y) / 2

	squareSize := srcImg.Bounds().Max.Y / 8

	squares := []squareInfo{}

	for rank := 1; rank <= 8; rank++ {
		for file := 'a'; file <= 'h'; file++ {
			name := fmt.Sprintf("%s%d", string([]byte{byte(file)}), rank)

			xStartOffset := int(('h' - file)) * squareSize
			yStartOffset := (rank - 1) * squareSize

			srcRect := image.Rect(
				xStartOffset+xOffset,
				yStartOffset,
				xStartOffset+xOffset+squareSize,
				yStartOffset+squareSize,
			)

			dstRect := image.Rect(
				xStartOffset,
				yStartOffset,
				xStartOffset+squareSize,
				yStartOffset+squareSize,
			)

			subPc, err := touch.PCLimitToImageBoxes(pc, []*image.Rectangle{&srcRect}, nil, props)
			if err != nil {
				return nil, nil, err
			}

			if subPc.Size() == 0 {
				return nil, nil, fmt.Errorf("pc for %s is empty in BoardDebugImageHack", name)
			}

			pieceColor := estimatePieceColor(subPc)
			colorNames := []string{"", "W", "B"}
			meta := colorNames[pieceColor]

			draw.Draw(dst, dstRect, srcImg, srcRect.Min, draw.Src)

			// put name in the middle of that square
			textX := dstRect.Min.X + squareSize/2 - len(name)*3
			textY := dstRect.Min.Y + squareSize/2 + 3
			drawString(dst, textX, textY, name+"-"+meta, color.RGBA{255, 0, 0, 255})

			squares = append(squares, squareInfo{
				rank,
				file,
				name,
				srcRect,
				pieceColor,
				subPc,
			})
		}
	}

	return dst, squares, nil
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

	_, span2 := trace.StartSpan(ctx, "PieceFinder::CaptureAllFromCamera::Images")
	ni, _, err := bc.input.Images(ctx, nil, extra)
	span2.End()
	if err != nil {
		return ret, err
	}

	_, span2 = trace.StartSpan(ctx, "PieceFinder::CaptureAllFromCamera::NextPointCloud")
	pc, err := bc.input.NextPointCloud(ctx, extra)
	span2.End()
	if err != nil {
		return ret, err
	}

	if len(ni) == 0 {
		return ret, fmt.Errorf("no images returned from input camera")
	}

	_, span2 = trace.StartSpan(ctx, "PieceFinder::CaptureAllFromCamera::Image")
	ret.Image, err = ni[0].Image(ctx)
	span2.End()
	if err != nil {
		return ret, err
	}

	_, span2 = trace.StartSpan(ctx, "PieceFinder::CaptureAllFromCamera::BoardDebugImageHack")
	dst, squares, err := BoardDebugImageHack(ret.Image, pc, bc.props)
	span2.End()
	if err != nil {
		return ret, err
	}

	_, span2 = trace.StartSpan(ctx, "PieceFinder::CaptureAllFromCamera::Finish")
	defer span2.End()

	if extra["printdst"] == true {
		err := rimage.WriteImageToFile("hack-test.jpg", dst)
		if err != nil {
			bc.logger.Warnf("Writing file failed: %v", err)
		}
	}

	ret.Objects = []*viz.Object{}
	ret.Detections = []objectdetection.Detection{}

	for _, s := range squares {
		pc, err := bc.rfs.TransformPointCloud(ctx, s.pc, bc.conf.Input, "world")
		if err != nil {
			return ret, err
		}

		if pc == nil {
			return ret, fmt.Errorf("why is pc nil")
		}

		label := fmt.Sprintf("%s-%d", s.name, s.color)
		o, err := viz.NewObjectWithLabel(pc, label, nil)
		if err != nil {
			return ret, err
		}

		if o.Geometry == nil {
			return ret, fmt.Errorf("why is Geometry nil for square: %s %v", s.name, s)
		}
		ret.Objects = append(ret.Objects, o)

		ret.Detections = append(ret.Detections, objectdetection.NewDetectionWithoutImgBounds(s.originalBounds, 1, label))

		lowPoint := touch.PCFindLowestInRegion(s.pc, image.Rect(-10000, -10000, 10000, 10000))

		lowX, lowY := bc.props.IntrinsicParams.PointToPixel(lowPoint.X, lowPoint.Y, lowPoint.Z)

		ret.Detections = append(ret.Detections,
			objectdetection.NewDetectionWithoutImgBounds(
				image.Rect(
					int(lowX-5),
					int(lowY-5),
					int(lowX+5),
					int(lowY+5),
				),
				1, "x-"+label))
	}

	return ret, nil
}

func (bc *PieceFinder) GetProperties(ctx context.Context, extra map[string]interface{}) (*vision.Properties, error) {
	return &vision.Properties{
		ObjectPCDsSupported: true,
	}, nil
}
