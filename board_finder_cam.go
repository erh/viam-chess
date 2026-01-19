package viamchess

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"

	"github.com/golang/geo/r3"

	"go.viam.com/rdk/components/camera"
	"go.viam.com/rdk/data"
	"go.viam.com/rdk/gostream"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/pointcloud"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/spatialmath"
	"go.viam.com/rdk/utils"
)

var BoardFinderCamModel = family.WithModel("board-finder-cam")

func init() {
	resource.RegisterComponent(camera.API, BoardFinderCamModel,
		resource.Registration[camera.Camera, *BoardFinderCamConfig]{
			Constructor: newBoardFinderCam,
		},
	)
}

type BoardFinderCamConfig struct {
	Camera     string `json:"camera"`
	OutputSize int    `json:"output_size"` // Size of the square output image (default 800)
}

func (cfg *BoardFinderCamConfig) Validate(path string) ([]string, []string, error) {
	if cfg.Camera == "" {
		return nil, nil, fmt.Errorf("need a camera")
	}
	return []string{cfg.Camera}, nil, nil
}

func newBoardFinderCam(ctx context.Context, deps resource.Dependencies, rawConf resource.Config, logger logging.Logger) (camera.Camera, error) {
	conf, err := resource.NativeConfig[*BoardFinderCamConfig](rawConf)
	if err != nil {
		return nil, err
	}

	cam, err := camera.FromDependencies(deps, conf.Camera)
	if err != nil {
		return nil, err
	}

	outputSize := conf.OutputSize
	if outputSize <= 0 {
		outputSize = 800
	}

	return &BoardFinderCam{
		name:       rawConf.ResourceName(),
		conf:       conf,
		logger:     logger,
		source:     cam,
		outputSize: outputSize,
	}, nil
}

type BoardFinderCam struct {
	resource.AlwaysRebuild
	resource.TriviallyCloseable

	name       resource.Name
	conf       *BoardFinderCamConfig
	logger     logging.Logger
	source     camera.Camera
	outputSize int
}

func (c *BoardFinderCam) Name() resource.Name {
	return c.name
}

func (c *BoardFinderCam) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	return nil, fmt.Errorf("DoCommand not supported")
}

func (c *BoardFinderCam) Geometries(ctx context.Context, extra map[string]interface{}) ([]spatialmath.Geometry, error) {
	return nil, nil
}

func (c *BoardFinderCam) Image(ctx context.Context, mimeType string, extra map[string]interface{}) ([]byte, camera.ImageMetadata, error) {
	img, err := c.getTransformedImage(ctx, extra)
	if err != nil {
		return nil, camera.ImageMetadata{}, err
	}

	// Encode the image as JPEG
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		return nil, camera.ImageMetadata{}, err
	}

	return buf.Bytes(), camera.ImageMetadata{MimeType: utils.MimeTypeJPEG}, nil
}

func (c *BoardFinderCam) Images(ctx context.Context, filterSourceNames []string, extra map[string]interface{}) ([]camera.NamedImage, resource.ResponseMetadata, error) {
	img, err := c.getTransformedImage(ctx, extra)
	if err != nil {
		return nil, resource.ResponseMetadata{}, err
	}

	namedImg, err := camera.NamedImageFromImage(img, c.name.Name, utils.MimeTypeJPEG, data.Annotations{})
	if err != nil {
		return nil, resource.ResponseMetadata{}, err
	}

	return []camera.NamedImage{namedImg}, resource.ResponseMetadata{}, nil
}

func (c *BoardFinderCam) Stream(ctx context.Context, errHandlers ...gostream.ErrorHandler) (gostream.VideoStream, error) {
	return gostream.NewEmbeddedVideoStreamFromReader(gostream.VideoReaderFunc(func(ctx context.Context) (image.Image, func(), error) {
		img, err := c.getTransformedImage(ctx, nil)
		if err != nil {
			return nil, nil, err
		}
		return img, func() {}, nil
	})), nil
}

func (c *BoardFinderCam) NextPointCloud(ctx context.Context, extra map[string]interface{}) (pointcloud.PointCloud, error) {
	// Get the pointcloud from the source camera
	pc, err := c.source.NextPointCloud(ctx, extra)
	if err != nil {
		return nil, fmt.Errorf("failed to get pointcloud from source: %w", err)
	}

	// Get the image and find corners
	imgs, _, err := c.source.Images(ctx, nil, extra)
	if err != nil {
		return nil, fmt.Errorf("failed to get images from source: %w", err)
	}

	if len(imgs) == 0 {
		return nil, fmt.Errorf("no images from source camera")
	}

	srcImg, err := imgs[0].Image(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %w", err)
	}

	// Find the board corners
	corners, err := findBoard(srcImg)
	if err != nil {
		return nil, fmt.Errorf("failed to find board: %w", err)
	}

	if len(corners) != 4 {
		return nil, fmt.Errorf("expected 4 corners, got %d", len(corners))
	}

	// Get camera properties for projection
	props, err := c.source.Properties(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get camera properties: %w", err)
	}

	if props.IntrinsicParams == nil {
		return nil, fmt.Errorf("camera does not have intrinsic parameters")
	}

	// Filter and transform the pointcloud so coordinates match the transformed image
	filtered, err := filterAndTransformPointCloud(pc, corners, c.outputSize, props)
	if err != nil {
		return nil, fmt.Errorf("failed to filter pointcloud: %w", err)
	}

	return filtered, nil
}

// filterAndTransformPointCloud filters a pointcloud to the board region and transforms
// the 3D coordinates so that when projected with the intrinsics, they land at the
// corresponding positions in the perspective-transformed image.
func filterAndTransformPointCloud(pc pointcloud.PointCloud, corners []image.Point, outputSize int, props camera.Properties) (pointcloud.PointCloud, error) {
	filtered := pointcloud.NewBasicEmpty()

	// Convert corners to float64 for point-in-polygon test
	polygon := make([]point, 4)
	for i, c := range corners {
		polygon[i] = point{float64(c.X), float64(c.Y)}
	}

	// Source corners (original image coordinates)
	srcPts := []point{
		{float64(corners[0].X), float64(corners[0].Y)},
		{float64(corners[1].X), float64(corners[1].Y)},
		{float64(corners[2].X), float64(corners[2].Y)},
		{float64(corners[3].X), float64(corners[3].Y)},
	}

	// Destination corners (transformed image coordinates)
	dstPts := []point{
		{0, 0},
		{float64(outputSize), 0},
		{float64(outputSize), float64(outputSize)},
		{0, float64(outputSize)},
	}

	// Compute perspective transform matrix (src -> dst)
	matrix := computePerspectiveMatrix(srcPts, dstPts)

	// Get intrinsic parameters
	fx := props.IntrinsicParams.Fx
	fy := props.IntrinsicParams.Fy
	ppx := props.IntrinsicParams.Ppx
	ppy := props.IntrinsicParams.Ppy

	pc.Iterate(0, 0, func(p r3.Vector, d pointcloud.Data) bool {
		// Project 3D point to original 2D coordinates
		imgX, imgY := props.IntrinsicParams.PointToPixel(p.X, p.Y, p.Z)

		// Check if inside board quadrilateral
		if !pointInPolygon(imgX, imgY, polygon) {
			return true
		}

		// Apply perspective transform to get new 2D coordinates
		newImgX, newImgY := applyPerspective(matrix, imgX, imgY)

		// Un-project to get new 3D coordinates that will project to (newImgX, newImgY)
		// Using: imgX = fx * X/Z + ppx, imgY = fy * Y/Z + ppy
		// So: X = (imgX - ppx) * Z / fx, Y = (imgY - ppy) * Z / fy
		newX := (newImgX - ppx) * p.Z / fx
		newY := (newImgY - ppy) * p.Z / fy

		newPoint := r3.Vector{X: newX, Y: newY, Z: p.Z}
		if d != nil {
			filtered.Set(newPoint, d)
		} else {
			filtered.Set(newPoint, nil)
		}
		return true
	})

	return filtered, nil
}

// pointInPolygon checks if a point (x, y) is inside a polygon using ray casting algorithm
func pointInPolygon(x, y float64, polygon []point) bool {
	n := len(polygon)
	inside := false

	j := n - 1
	for i := 0; i < n; i++ {
		xi, yi := polygon[i].x, polygon[i].y
		xj, yj := polygon[j].x, polygon[j].y

		if ((yi > y) != (yj > y)) && (x < (xj-xi)*(y-yi)/(yj-yi)+xi) {
			inside = !inside
		}
		j = i
	}

	return inside
}

func (c *BoardFinderCam) Properties(ctx context.Context) (camera.Properties, error) {
	// Get source camera properties for intrinsics
	srcProps, err := c.source.Properties(ctx)
	if err != nil {
		return camera.Properties{
			SupportsPCD: false,
			ImageType:   camera.ColorStream,
		}, nil
	}

	return camera.Properties{
		SupportsPCD:      srcProps.SupportsPCD,
		ImageType:        camera.ColorStream,
		IntrinsicParams:  srcProps.IntrinsicParams,
		DistortionParams: srcProps.DistortionParams,
	}, nil
}

func (c *BoardFinderCam) getTransformedImage(ctx context.Context, extra map[string]interface{}) (image.Image, error) {
	imgs, _, err := c.source.Images(ctx, nil, extra)
	if err != nil {
		return nil, err
	}

	if len(imgs) == 0 {
		return nil, fmt.Errorf("no images from source camera")
	}

	srcImg, err := imgs[0].Image(ctx)
	if err != nil {
		return nil, err
	}

	// Find the board corners
	corners, err := findBoard(srcImg)
	if err != nil {
		return nil, fmt.Errorf("failed to find board: %w", err)
	}

	if len(corners) != 4 {
		return nil, fmt.Errorf("expected 4 corners, got %d", len(corners))
	}

	// Perform perspective transform
	// corners are: top-left, top-right, bottom-right, bottom-left
	dst := perspectiveTransform(srcImg, corners, c.outputSize)

	return dst, nil
}

// perspectiveTransform applies a perspective transformation to extract the board region
// and map it to a square output image
func perspectiveTransform(src image.Image, corners []image.Point, outputSize int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, outputSize, outputSize))

	// Source corners (from findBoard): top-left, top-right, bottom-right, bottom-left
	srcPts := []point{
		{float64(corners[0].X), float64(corners[0].Y)},
		{float64(corners[1].X), float64(corners[1].Y)},
		{float64(corners[2].X), float64(corners[2].Y)},
		{float64(corners[3].X), float64(corners[3].Y)},
	}

	// Destination corners (square output)
	dstPts := []point{
		{0, 0},
		{float64(outputSize), 0},
		{float64(outputSize), float64(outputSize)},
		{0, float64(outputSize)},
	}

	// Compute the inverse transformation matrix (dst -> src)
	// We need to map destination pixels to source pixels
	matrix := computePerspectiveMatrix(dstPts, srcPts)

	bounds := src.Bounds()

	for y := 0; y < outputSize; y++ {
		for x := 0; x < outputSize; x++ {
			// Apply inverse transform to find source coordinates
			srcX, srcY := applyPerspective(matrix, float64(x), float64(y))

			// Bilinear interpolation
			c := bilinearSample(src, srcX, srcY, bounds)
			dst.Set(x, y, c)
		}
	}

	return dst
}

type point struct {
	x, y float64
}

// computePerspectiveMatrix computes the 3x3 perspective transformation matrix
// that maps src points to dst points
func computePerspectiveMatrix(src, dst []point) [9]float64 {
	// Build the 8x8 system of equations for perspective transform
	// For each point pair: dst = H * src (in homogeneous coordinates)
	// x' = (h0*x + h1*y + h2) / (h6*x + h7*y + 1)
	// y' = (h3*x + h4*y + h5) / (h6*x + h7*y + 1)

	var A [8][8]float64
	var b [8]float64

	for i := 0; i < 4; i++ {
		sx, sy := src[i].x, src[i].y
		dx, dy := dst[i].x, dst[i].y

		A[i*2][0] = sx
		A[i*2][1] = sy
		A[i*2][2] = 1
		A[i*2][3] = 0
		A[i*2][4] = 0
		A[i*2][5] = 0
		A[i*2][6] = -dx * sx
		A[i*2][7] = -dx * sy
		b[i*2] = dx

		A[i*2+1][0] = 0
		A[i*2+1][1] = 0
		A[i*2+1][2] = 0
		A[i*2+1][3] = sx
		A[i*2+1][4] = sy
		A[i*2+1][5] = 1
		A[i*2+1][6] = -dy * sx
		A[i*2+1][7] = -dy * sy
		b[i*2+1] = dy
	}

	// Solve using Gaussian elimination
	h := solveLinearSystem(A, b)

	return [9]float64{h[0], h[1], h[2], h[3], h[4], h[5], h[6], h[7], 1}
}

// solveLinearSystem solves Ax = b using Gaussian elimination with partial pivoting
func solveLinearSystem(A [8][8]float64, b [8]float64) [8]float64 {
	// Forward elimination
	for col := 0; col < 8; col++ {
		// Find pivot
		maxRow := col
		for row := col + 1; row < 8; row++ {
			if absFloat(A[row][col]) > absFloat(A[maxRow][col]) {
				maxRow = row
			}
		}

		// Swap rows
		A[col], A[maxRow] = A[maxRow], A[col]
		b[col], b[maxRow] = b[maxRow], b[col]

		// Eliminate
		for row := col + 1; row < 8; row++ {
			if A[col][col] == 0 {
				continue
			}
			factor := A[row][col] / A[col][col]
			for k := col; k < 8; k++ {
				A[row][k] -= factor * A[col][k]
			}
			b[row] -= factor * b[col]
		}
	}

	// Back substitution
	var x [8]float64
	for i := 7; i >= 0; i-- {
		x[i] = b[i]
		for j := i + 1; j < 8; j++ {
			x[i] -= A[i][j] * x[j]
		}
		if A[i][i] != 0 {
			x[i] /= A[i][i]
		}
	}

	return x
}

func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// applyPerspective applies the perspective transformation matrix to a point
func applyPerspective(m [9]float64, x, y float64) (float64, float64) {
	w := m[6]*x + m[7]*y + m[8]
	if w == 0 {
		w = 1
	}
	newX := (m[0]*x + m[1]*y + m[2]) / w
	newY := (m[3]*x + m[4]*y + m[5]) / w
	return newX, newY
}

// bilinearSample samples the source image using bilinear interpolation
func bilinearSample(src image.Image, x, y float64, bounds image.Rectangle) color.Color {
	x0 := int(x)
	y0 := int(y)
	x1 := x0 + 1
	y1 := y0 + 1

	// Clamp to bounds
	if x0 < bounds.Min.X {
		x0 = bounds.Min.X
	}
	if y0 < bounds.Min.Y {
		y0 = bounds.Min.Y
	}
	if x1 >= bounds.Max.X {
		x1 = bounds.Max.X - 1
	}
	if y1 >= bounds.Max.Y {
		y1 = bounds.Max.Y - 1
	}
	if x0 >= bounds.Max.X {
		x0 = bounds.Max.X - 1
	}
	if y0 >= bounds.Max.Y {
		y0 = bounds.Max.Y - 1
	}

	// Fractional parts
	fx := x - float64(x0)
	fy := y - float64(y0)

	// Sample four corners
	c00 := src.At(x0, y0)
	c10 := src.At(x1, y0)
	c01 := src.At(x0, y1)
	c11 := src.At(x1, y1)

	r00, g00, b00, a00 := c00.RGBA()
	r10, g10, b10, a10 := c10.RGBA()
	r01, g01, b01, a01 := c01.RGBA()
	r11, g11, b11, a11 := c11.RGBA()

	// Bilinear interpolation
	r := uint8((float64(r00)*(1-fx)*(1-fy) + float64(r10)*fx*(1-fy) + float64(r01)*(1-fx)*fy + float64(r11)*fx*fy) / 256)
	g := uint8((float64(g00)*(1-fx)*(1-fy) + float64(g10)*fx*(1-fy) + float64(g01)*(1-fx)*fy + float64(g11)*fx*fy) / 256)
	b := uint8((float64(b00)*(1-fx)*(1-fy) + float64(b10)*fx*(1-fy) + float64(b01)*(1-fx)*fy + float64(b11)*fx*fy) / 256)
	a := uint8((float64(a00)*(1-fx)*(1-fy) + float64(a10)*fx*(1-fy) + float64(a01)*(1-fx)*fy + float64(a11)*fx*fy) / 256)

	return color.RGBA{R: r, G: g, B: b, A: a}
}

// Ensure BoardFinderCam implements camera.Camera
var _ camera.Camera = (*BoardFinderCam)(nil)
