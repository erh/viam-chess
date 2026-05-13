package viamchess

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"os"
	"sort"
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

// classifyConfig holds the thresholds used by piece-classification helpers.
type classifyConfig struct {
	// MinPieceSize is the minimum piece height above the board surface (mm).
	// Points within this band from the top of the point cloud are treated as
	// the "top band" used for color classification.
	MinPieceSize float64

	// SquareInset is the number of pixels to shrink each square's bounding
	// rectangle inward on each side, to avoid border lines and depth/RGB
	// alignment artefacts.
	SquareInset float64

	// OtsuSeparationThreshold is the minimum between-class mean separation
	// required by the 2D Otsu classifier to declare a piece present.
	OtsuSeparationThreshold float64

	// ColorDivergenceGuard is the maximum tolerated per-channel average
	// divergence between point-cloud-attached colors and the projected srcImg
	// pixel colors. Exceeding this rejects the 3D verdict.
	ColorDivergenceGuard float64

	// MinTopFootprintMM is the minimum 2D extent (mm) the top-band points
	// must span in both x and y for the 3D verdict to be trusted.
	MinTopFootprintMM float64

	// BrightnessThreshold is the mean-RGB cutoff between black (color=2) and
	// white (color=1). Pieces in real chess sets are often cream/ivory rather
	// than pure white, averaging brightness ~115–135 under typical lighting,
	// so the historical default of 128 sits right where borderline pieces
	// flip frame-to-frame. Tune lower (e.g. 110) if light pieces classify as
	// black; raise if shadows on dark pieces classify as white.
	BrightnessThreshold float64
}

func defaultClassifyConfig() classifyConfig {
	return classifyConfig{
		MinPieceSize:            25.0,
		SquareInset:             10.0,
		OtsuSeparationThreshold: 25.0,
		ColorDivergenceGuard:    60.0,
		MinTopFootprintMM:       5.0,
		BrightnessThreshold:     128.0,
	}
}

func init() {
	resource.RegisterService(vision.API, PieceFinderModel,
		resource.Registration[vision.Service, *PieceFinderConfig]{
			Constructor: newPieceFinder,
		},
	)
}

type PieceFinderConfig struct {
	Input string // this is the cropped camera for the board, TODO: what orientation???

	MinPieceSize            float64 `json:"min-piece-size"`            // default 25.0 mm
	SquareInset             float64 `json:"square-inset"`              // default 10.0 px
	OtsuSeparationThreshold float64 `json:"otsu-separation-threshold"` // default 25.0
	ColorDivergenceGuard    float64 `json:"color-divergence-guard"`    // default 60.0
	MinTopFootprintMM       float64 `json:"min-top-footprint-mm"`      // default 5.0 mm
	BrightnessThreshold     float64 `json:"brightness-threshold"`      // default 128.0 (mean RGB)
}

func (cfg *PieceFinderConfig) Validate(path string) ([]string, []string, error) {
	if cfg.Input == "" {
		return nil, nil, fmt.Errorf("need an input")
	}
	return []string{cfg.Input}, nil, nil
}

func (cfg *PieceFinderConfig) toClassifyConfig() classifyConfig {
	cc := defaultClassifyConfig()
	if cfg.MinPieceSize > 0 {
		cc.MinPieceSize = cfg.MinPieceSize
	}
	if cfg.SquareInset > 0 {
		cc.SquareInset = cfg.SquareInset
	}
	if cfg.OtsuSeparationThreshold > 0 {
		cc.OtsuSeparationThreshold = cfg.OtsuSeparationThreshold
	}
	if cfg.ColorDivergenceGuard > 0 {
		cc.ColorDivergenceGuard = cfg.ColorDivergenceGuard
	}
	if cfg.MinTopFootprintMM > 0 {
		cc.MinTopFootprintMM = cfg.MinTopFootprintMM
	}
	if cfg.BrightnessThreshold > 0 {
		cc.BrightnessThreshold = cfg.BrightnessThreshold
	}
	return cc
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

func computeSquareBounds(corners []image.Point, col, row int, squareInset float64) image.Rectangle {

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
	inset := min(int(squareInset), (bounds.Max.X-bounds.Min.X)/10)
	bounds.Min.X += inset
	bounds.Min.Y += inset
	bounds.Max.X -= inset
	bounds.Max.Y -= inset

	return bounds
}

func findBoardAndPieces(ctx context.Context, srcImg image.Image, pc pointcloud.PointCloud, props camera.Properties, logger logging.Logger, cc classifyConfig) ([]squareInfo, error) {

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
			srcRect := computeSquareBounds(corners, int('h'-file), rank-1, cc.SquareInset)
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

	// Phase 3: estimate piece color for each square.
	_, span = trace.StartSpan(ctx, "PieceFinder::findBoardAndPieces::EstimateColors")
	for i := range squares {
		if subPcs[i].Size() == 0 {
			logger.Debugf("pc for %s is empty, will use 2D fallback", squares[i].name)
		}
		squares[i].color = classifyPieceColor(subPcs[i], srcImg, squares[i].originalBounds, props, cc)
		squares[i].pc = subPcs[i]
	}
	span.End()

	return squares, nil
}

// boardPlaneZ estimates the board's surface z from the median of the point
// cloud's z values. pc.MetaData().MaxZ is unreliable when stray wall/floor
// points behind the board leak into the per-square pc (camera angle, gaps
// at the board edge) — those outliers push MaxZ deeper than the actual
// board surface and corrupt the "top band" threshold used by piece detection.
// The board is the dominant cluster in every realistic per-square pc (it
// always covers most of a square, even with a piece sitting on it), so
// the median lands on it. Returns the actual MaxZ as a fallback when the
// pc has no points.
func boardPlaneZ(pc pointcloud.PointCloud) float64 {
	var zs []float64
	pc.Iterate(0, 0, func(p r3.Vector, d pointcloud.Data) bool {
		zs = append(zs, p.Z)
		return true
	})
	if len(zs) == 0 {
		return pc.MetaData().MaxZ
	}
	sort.Float64s(zs)
	return zs[len(zs)/2]
}

type pointSample struct {
	X, Y, Z                         float64
	PixelX, PixelY                  int
	AttachedR, AttachedG, AttachedB uint8
	ImgR, ImgG, ImgB                uint8
}

func (s pointSample) asMap() map[string]interface{} {
	return map[string]interface{}{
		"x":          s.X,
		"y":          s.Y,
		"z":          s.Z,
		"pixel_x":    s.PixelX,
		"pixel_y":    s.PixelY,
		"attached_r": int(s.AttachedR),
		"attached_g": int(s.AttachedG),
		"attached_b": int(s.AttachedB),
		"img_r":      int(s.ImgR),
		"img_g":      int(s.ImgG),
		"img_b":      int(s.ImgB),
	}
}

// pcDiag3DExtra captures the full 3D context around a bucket's classification:
// the pc's spatial extent, the top-band subset's extent, a per-channel comparison
// between colors baked into the pointcloud and colors read from srcImg at the
// points' projected pixels, and up to maxSamples individual rows of both.
type pcDiag3DExtra struct {
	TotalCount       int
	MinX, MaxX       float64
	MinY, MaxY       float64
	MinZ, MaxZ       float64
	BoardPlaneZ      float64
	TopCount         int
	TopColoredCount  int
	TopMinX, TopMaxX float64
	TopMinY, TopMaxY float64
	TopMinZ, TopMaxZ float64

	TopMeanAttachedR   float64
	TopMeanAttachedG   float64
	TopMeanAttachedB   float64
	TopMeanImgR        float64
	TopMeanImgG        float64
	TopMeanImgB        float64
	TopColorDivergence float64

	// Board-surface band stats: points with z within boardBandHalfMM of the
	// board plane, i.e. the visible square color around the piece's footprint.
	// Used as a lighting-invariant reference for the piece-color classifier.
	BoardCount         int
	BoardColoredCount  int
	BoardMeanAttachedR float64
	BoardMeanAttachedG float64
	BoardMeanAttachedB float64

	Samples []pointSample
}

// rejectReason returns a short human-readable reason if the 3D verdict should
// be rejected by the guards, or "" if the verdict is trusted.
func (d pcDiag3DExtra) rejectReason(colorDivGuard, minFootprintMM float64) string {
	if d.TopColorDivergence > colorDivGuard {
		return fmt.Sprintf("color divergence %.1f > %.1f", d.TopColorDivergence, colorDivGuard)
	}
	fpX := d.TopMaxX - d.TopMinX
	fpY := d.TopMaxY - d.TopMinY
	if fpX < minFootprintMM || fpY < minFootprintMM {
		return fmt.Sprintf("top footprint %.1fx%.1f mm below %.1f mm", fpX, fpY, minFootprintMM)
	}
	return ""
}

// classifyPieceColor returns 0 (empty), 1 (white), or 2 (black). 3D color is
// compared relative to the square's own board-surface points so the verdict is
// lighting-invariant; warmth (R-B) is the tiebreaker. Falls back to 2D Otsu
// when the 3D signal is missing or its colors diverge from srcImg.
func classifyPieceColor(pc pointcloud.PointCloud, img image.Image, rect image.Rectangle, props camera.Properties, cc classifyConfig) int {
	d3x := pcDiagnose3D(pc, img, props, 0, cc.MinPieceSize)

	// pc-attached colors disagree with srcImg pixels → bad depth/RGB alignment.
	if d3x.TopColoredCount > 0 && d3x.TopColorDivergence > cc.ColorDivergenceGuard {
		return colorFromImage2D(img, rect, cc.OtsuSeparationThreshold).Color
	}

	if d3x.TopColoredCount > 5 {
		pieceBr := (d3x.TopMeanAttachedR + d3x.TopMeanAttachedG + d3x.TopMeanAttachedB) / 3.0
		// Cream pieces keep R-B > 15 at any brightness; black plastic is neutral (R-B < 5).
		// Warmth survives both dim lighting and dark-green boards where brightness alone lies.
		pieceWarmth := d3x.TopMeanAttachedR - d3x.TopMeanAttachedB
		const clearWhiteDiff = 25.0
		const clearBlackDiff = -50.0
		const warmthCutoff = 10.0
		const absoluteWhiteCutoff = 100.0
		if d3x.BoardColoredCount > 10 {
			boardBr := (d3x.BoardMeanAttachedR + d3x.BoardMeanAttachedG + d3x.BoardMeanAttachedB) / 3.0
			diff := pieceBr - boardBr
			if diff > clearWhiteDiff {
				return 1
			}
			if diff < clearBlackDiff {
				return 2
			}
			if pieceWarmth > warmthCutoff {
				return 1
			}
			return 2
		}
		if pieceBr > absoluteWhiteCutoff || pieceWarmth > warmthCutoff {
			return 1
		}
		return 2
	}

	// Depth can wholly miss glossy/low-texture pieces while still capturing the
	// surrounding board, so total_count alone can't rule out a piece. 2D Otsu
	// returns 0 for uniform rects.
	return colorFromImage2D(img, rect, cc.OtsuSeparationThreshold).Color
}

func (d pcDiag3DExtra) asMap() map[string]interface{} {
	samples := make([]map[string]interface{}, 0, len(d.Samples))
	for _, s := range d.Samples {
		samples = append(samples, s.asMap())
	}
	return map[string]interface{}{
		"total_count":          d.TotalCount,
		"min_x":                d.MinX,
		"max_x":                d.MaxX,
		"min_y":                d.MinY,
		"max_y":                d.MaxY,
		"min_z":                d.MinZ,
		"max_z":                d.MaxZ,
		"board_plane_z":        d.BoardPlaneZ,
		"top_count":            d.TopCount,
		"top_colored_count":    d.TopColoredCount,
		"top_min_x":            d.TopMinX,
		"top_max_x":            d.TopMaxX,
		"top_min_y":            d.TopMinY,
		"top_max_y":            d.TopMaxY,
		"top_min_z":            d.TopMinZ,
		"top_max_z":            d.TopMaxZ,
		"top_mean_attached_r":  d.TopMeanAttachedR,
		"top_mean_attached_g":  d.TopMeanAttachedG,
		"top_mean_attached_b":  d.TopMeanAttachedB,
		"top_mean_img_r":       d.TopMeanImgR,
		"top_mean_img_g":       d.TopMeanImgG,
		"top_mean_img_b":       d.TopMeanImgB,
		"top_color_divergence": d.TopColorDivergence,
		"samples":              samples,
	}
}

func pcDiagnose3D(pc pointcloud.PointCloud, img image.Image, props camera.Properties, maxSamples int, minPieceSize float64) pcDiag3DExtra {
	out := pcDiag3DExtra{TotalCount: pc.Size()}
	if pc.Size() == 0 {
		return out
	}
	out.MinX, out.MaxX = math.Inf(1), math.Inf(-1)
	out.MinY, out.MaxY = math.Inf(1), math.Inf(-1)
	out.MinZ, out.MaxZ = math.Inf(1), math.Inf(-1)
	out.TopMinX, out.TopMaxX = math.Inf(1), math.Inf(-1)
	out.TopMinY, out.TopMaxY = math.Inf(1), math.Inf(-1)
	out.TopMinZ, out.TopMaxZ = math.Inf(1), math.Inf(-1)

	// pc.MetaData().MaxZ can be polluted by stray wall/floor points behind the
	// board, which would corrupt the "top band" threshold. Use the median of
	// the per-square z values as the board-plane estimate instead — the board
	// is always the dominant cluster in a per-square pc.
	boardZ := boardPlaneZ(pc)
	out.BoardPlaneZ = boardZ
	minZCutoff := boardZ - minPieceSize
	imgBounds := img.Bounds()
	// Points within boardBandHalfMM of boardZ are "on the board surface" — used
	// to estimate the visible square color around the piece for the relative
	// W/B classifier.
	const boardBandHalfMM = 5.0

	var sumAR, sumAG, sumAB, sumIR, sumIG, sumIB, sumDiv float64
	topColored := 0
	// Top points whose projected pixel lands inside the source image. Only these
	// contribute to sumIR/IG/IB/sumDiv — using `topColored` as the denominator
	// for the img-side means would underestimate them (and dilute divergence
	// below the rejection guard) when some top points project out-of-image.
	inImgCount := 0
	var sumBoardR, sumBoardG, sumBoardB float64

	pc.Iterate(0, 0, func(p r3.Vector, d pointcloud.Data) bool {
		if p.X < out.MinX {
			out.MinX = p.X
		}
		if p.X > out.MaxX {
			out.MaxX = p.X
		}
		if p.Y < out.MinY {
			out.MinY = p.Y
		}
		if p.Y > out.MaxY {
			out.MaxY = p.Y
		}
		if p.Z < out.MinZ {
			out.MinZ = p.Z
		}
		if p.Z > out.MaxZ {
			out.MaxZ = p.Z
		}

		if p.Z >= minZCutoff {
			// Board-surface band: |z - boardZ| < boardBandHalfMM.
			if p.Z >= boardZ-boardBandHalfMM && p.Z <= boardZ+boardBandHalfMM {
				out.BoardCount++
				if d != nil && d.HasColor() {
					br, bg, bb := d.RGB255()
					sumBoardR += float64(br)
					sumBoardG += float64(bg)
					sumBoardB += float64(bb)
					out.BoardColoredCount++
				}
			}
			return true
		}

		if p.X < out.TopMinX {
			out.TopMinX = p.X
		}
		if p.X > out.TopMaxX {
			out.TopMaxX = p.X
		}
		if p.Y < out.TopMinY {
			out.TopMinY = p.Y
		}
		if p.Y > out.TopMaxY {
			out.TopMaxY = p.Y
		}
		if p.Z < out.TopMinZ {
			out.TopMinZ = p.Z
		}
		if p.Z > out.TopMaxZ {
			out.TopMaxZ = p.Z
		}
		out.TopCount++

		if d == nil || !d.HasColor() {
			return true
		}
		pr, pg, pb := d.RGB255()
		px, py, err := props.PointToPixel(p)
		if err != nil {
			return true
		}
		ix, iy := int(px), int(py)

		var ir, ig, ib uint8
		inImg := ix >= imgBounds.Min.X && ix < imgBounds.Max.X && iy >= imgBounds.Min.Y && iy < imgBounds.Max.Y
		if inImg {
			cr, cg, cb, _ := img.At(ix, iy).RGBA()
			ir = uint8(cr >> 8)
			ig = uint8(cg >> 8)
			ib = uint8(cb >> 8)
		}

		sumAR += float64(pr)
		sumAG += float64(pg)
		sumAB += float64(pb)
		if inImg {
			sumIR += float64(ir)
			sumIG += float64(ig)
			sumIB += float64(ib)
			sumDiv += (math.Abs(float64(pr)-float64(ir)) +
				math.Abs(float64(pg)-float64(ig)) +
				math.Abs(float64(pb)-float64(ib))) / 3.0
			inImgCount++
		}
		topColored++
		out.TopColoredCount++

		if len(out.Samples) < maxSamples {
			out.Samples = append(out.Samples, pointSample{
				X: p.X, Y: p.Y, Z: p.Z,
				PixelX: ix, PixelY: iy,
				AttachedR: pr, AttachedG: pg, AttachedB: pb,
				ImgR: ir, ImgG: ig, ImgB: ib,
			})
		}
		return true
	})

	if topColored > 0 {
		out.TopMeanAttachedR = sumAR / float64(topColored)
		out.TopMeanAttachedG = sumAG / float64(topColored)
		out.TopMeanAttachedB = sumAB / float64(topColored)
	}
	if inImgCount > 0 {
		out.TopMeanImgR = sumIR / float64(inImgCount)
		out.TopMeanImgG = sumIG / float64(inImgCount)
		out.TopMeanImgB = sumIB / float64(inImgCount)
		out.TopColorDivergence = sumDiv / float64(inImgCount)
	}

	if out.BoardColoredCount > 0 {
		out.BoardMeanAttachedR = sumBoardR / float64(out.BoardColoredCount)
		out.BoardMeanAttachedG = sumBoardG / float64(out.BoardColoredCount)
		out.BoardMeanAttachedB = sumBoardB / float64(out.BoardColoredCount)
	}

	if out.TopCount == 0 {
		out.TopMinX, out.TopMaxX = 0, 0
		out.TopMinY, out.TopMaxY = 0, 0
		out.TopMinZ, out.TopMaxZ = 0, 0
	}
	return out
}

type colorDiag2D struct {
	Total      int
	Threshold  int
	MeanDark   float64
	MeanLight  float64
	CntDark    int
	CntLight   int
	Separation float64
	Color      int
}

func (d colorDiag2D) asMap() map[string]interface{} {
	return map[string]interface{}{
		"total":      d.Total,
		"threshold":  d.Threshold,
		"mean_dark":  d.MeanDark,
		"mean_light": d.MeanLight,
		"cnt_dark":   d.CntDark,
		"cnt_light":  d.CntLight,
		"separation": d.Separation,
		"color":      d.Color,
	}
}

// colorFromImage2D runs Otsu's threshold on the 2D image region and returns
// all intermediate values alongside the classification (0 empty, 1 white, 2 black).
func colorFromImage2D(img image.Image, rect image.Rectangle, otsuSepThresh float64) colorDiag2D {
	var hist [256]int
	total := 0
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			// RGBA returns [0,65535]; shift to [0,255].
			gray := (299*int(r>>8) + 587*int(g>>8) + 114*int(b>>8)) / 1000
			if gray > 255 {
				gray = 255
			}
			hist[gray]++
			total++
		}
	}
	diag := colorDiag2D{Total: total}
	if total == 0 {
		return diag
	}

	var sumAll float64
	for i, n := range hist {
		sumAll += float64(i) * float64(n)
	}
	var sumB float64
	var wB int
	maxVar := 0.0
	threshold := 0
	for t := 0; t < 256; t++ {
		wB += hist[t]
		if wB == 0 {
			continue
		}
		wF := total - wB
		if wF == 0 {
			break
		}
		sumB += float64(t) * float64(hist[t])
		meanB := sumB / float64(wB)
		meanF := (sumAll - sumB) / float64(wF)
		v := float64(wB) * float64(wF) * (meanB - meanF) * (meanB - meanF)
		if v > maxVar {
			maxVar = v
			threshold = t
		}
	}
	diag.Threshold = threshold

	var sumDark, sumLight float64
	var cntDark, cntLight int
	for i, n := range hist {
		if i <= threshold {
			sumDark += float64(i) * float64(n)
			cntDark += n
		} else {
			sumLight += float64(i) * float64(n)
			cntLight += n
		}
	}
	diag.CntDark = cntDark
	diag.CntLight = cntLight
	if cntDark == 0 || cntLight == 0 {
		return diag
	}
	diag.MeanDark = sumDark / float64(cntDark)
	diag.MeanLight = sumLight / float64(cntLight)
	diag.Separation = diag.MeanLight - diag.MeanDark

	// Low separation means a uniform square; lighting-robust because both
	// class means shift together under illumination changes.
	if diag.Separation < otsuSepThresh {
		return diag
	}
	// Empty-square guard: Otsu will happily find some bimodal split even on
	// an empty square (shadow at the rect edge, a few stray noisy pixels),
	// and the separation can edge over the threshold. A real piece occupies
	// 20-50% of the inset rect, so a sub-5% minority class is noise, not a
	// piece. (g4 in the failure case: 57/4968 = 1.15% dark.)
	minorityCnt := cntDark
	if cntLight < minorityCnt {
		minorityCnt = cntLight
	}
	if float64(minorityCnt)/float64(diag.Total) < 0.05 {
		return diag
	}
	// Minority class is the piece — board-color-invariant, unlike "more extreme class".
	if cntDark < cntLight {
		diag.Color = 2
	} else {
		diag.Color = 1
	}
	return diag
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
	action, _ := cmd["cmd"].(string)
	switch action {
	case "diagnose":
		square, _ := cmd["square"].(string)
		saveDebug, _ := cmd["save_debug"].(bool)
		samples := 10
		if v, ok := cmd["samples"].(float64); ok && v > 0 {
			samples = int(v)
		}
		return bc.diagnose(ctx, square, samples, saveDebug)
	default:
		return nil, fmt.Errorf("unknown command %q", action)
	}
}

// diagnose captures a fresh frame, partitions it into 64 squares, and returns
// the full 3D and 2D color-classification intermediates per square plus a
// per-point comparison between the colors baked into the pointcloud and the
// colors in srcImg at those points' projected pixels. Pass a "square" filter
// to restrict output, "samples" to cap the per-square sample count, and
// "save_debug" to write the RGB frame, annotated rect, and sub-PCD to disk.
func (bc *PieceFinder) diagnose(ctx context.Context, filter string, samples int, saveDebug bool) (map[string]interface{}, error) {
	ni, _, err := bc.input.Images(ctx, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("images: %w", err)
	}
	if len(ni) == 0 {
		return nil, fmt.Errorf("no images returned")
	}
	pc, err := bc.input.NextPointCloud(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("pointcloud: %w", err)
	}
	img, err := ni[0].Image(ctx)
	if err != nil {
		return nil, err
	}

	corners, err := findBoard(img)
	if err != nil {
		return nil, fmt.Errorf("findBoard: %w", err)
	}

	cc := bc.conf.toClassifyConfig()

	type bucket struct {
		name   string
		bounds image.Rectangle
		pc     pointcloud.PointCloud
	}
	buckets := make([]bucket, 0, 64)
	for rank := 1; rank <= 8; rank++ {
		for file := 'a'; file <= 'h'; file++ {
			name := fmt.Sprintf("%s%d", string([]byte{byte(file)}), rank)
			rect := computeSquareBounds(corners, int('h'-file), rank-1, cc.SquareInset)
			buckets = append(buckets, bucket{name: name, bounds: rect, pc: pointcloud.NewBasicEmpty()})
		}
	}

	pc.Iterate(0, 0, func(p r3.Vector, d pointcloud.Data) bool {
		x, y, perr := bc.props.PointToPixel(p)
		if perr != nil {
			return false
		}
		ix, iy := int(x), int(y)
		for i := range buckets {
			b := buckets[i].bounds
			if ix >= b.Min.X && ix <= b.Max.X && iy >= b.Min.Y && iy <= b.Max.Y {
				buckets[i].pc.Set(p, d)
				break
			}
		}
		return true
	})

	debugFiles := []string{}
	if saveDebug {
		if err := rimage.SaveImage(img, "piece-finder-diag.jpg"); err != nil {
			bc.logger.Warnf("save rgb: %v", err)
		} else {
			debugFiles = append(debugFiles, "piece-finder-diag.jpg")
		}
		if f, err := os.Create("piece-finder-diag.pcd"); err != nil {
			bc.logger.Warnf("create full pcd: %v", err)
		} else {
			if err := pointcloud.ToPCD(pc, f, pointcloud.PCDBinary); err != nil {
				bc.logger.Warnf("write full pcd: %v", err)
			}
			f.Close()
			debugFiles = append(debugFiles, "piece-finder-diag.pcd")
		}
	}

	results := make([]map[string]interface{}, 0, len(buckets))
	for _, b := range buckets {
		if filter != "" && b.name != filter {
			continue
		}
		d2 := colorFromImage2D(img, b.bounds, cc.OtsuSeparationThreshold)
		d3x := pcDiagnose3D(b.pc, img, bc.props, samples, cc.MinPieceSize)
		rejectReason := d3x.rejectReason(cc.ColorDivergenceGuard, cc.MinTopFootprintMM)
		// Call the production classifier so the diagnostic mirrors the real
		// algorithm — the diff-based 3D rule that respects board-band color
		// and tolerates rejected footprints.
		final := classifyPieceColor(b.pc, img, b.bounds, bc.props, cc)

		row := map[string]interface{}{
			"square":           b.name,
			"bounds":           []int{b.bounds.Min.X, b.bounds.Min.Y, b.bounds.Max.X, b.bounds.Max.Y},
			"pc_size":          b.pc.Size(),
			"d2":               d2.asMap(),
			"d3x":              d3x.asMap(),
			"final_color":      final,
			"3d_reject_reason": rejectReason,
		}

		if saveDebug {
			rectPath := fmt.Sprintf("piece-finder-diag-%s-rect.jpg", b.name)
			if err := saveAnnotatedImage(img, b.bounds, rectPath); err != nil {
				bc.logger.Warnf("save annotated: %v", err)
			} else {
				row["annotated_image"] = rectPath
			}
			pcdPath := fmt.Sprintf("piece-finder-diag-%s.pcd", b.name)
			if f, err := os.Create(pcdPath); err != nil {
				bc.logger.Warnf("create sub pcd: %v", err)
			} else {
				if err := pointcloud.ToPCD(b.pc, f, pointcloud.PCDBinary); err != nil {
					bc.logger.Warnf("write sub pcd: %v", err)
				}
				f.Close()
				row["sub_pcd"] = pcdPath
			}
		}

		results = append(results, row)
	}

	return map[string]interface{}{
		"squares":     results,
		"debug_files": debugFiles,
	}, nil
}

func saveAnnotatedImage(src image.Image, rect image.Rectangle, path string) error {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, src, image.Point{}, draw.Src)
	drawRect(dst, rect, color.RGBA{255, 0, 0, 255})
	return rimage.SaveImage(dst, path)
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
	squares, err := findBoardAndPieces(ctx, ret.Image, pc, bc.props, bc.logger, bc.conf.toClassifyConfig())
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
