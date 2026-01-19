package viamchess

import (
	"image"
	"math"
	"sort"
)

// findBoard finds the four corners of the chess board.
// Detects the boundary where the checkerboard pattern begins.
func findBoard(img image.Image) ([]image.Point, error) {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	// Step 1: Create grayscale image
	gray := makeGrayImage(img)

	// Step 2: Find board region using color-based detection
	boardMask := createBoardMaskColor(img, width, height)

	// Step 3: Find the boundary of the masked region
	boundaryPoints := findBoundary(boardMask)

	if len(boundaryPoints) < 100 {
		return defaultCorners(width, height), nil
	}

	// Step 4: Find corners by looking for extreme points in each direction
	corners := findExtremeCorners(boundaryPoints)

	// Step 5: Move corners inward to find the actual checkerboard start
	corners = findCheckerboardStart(corners, img, gray, width, height)

	// Step 6: Refine corners using line detection for precision
	corners = refineCornersWithLines(gray, corners, width, height)

	return corners, nil
}

// createBoardMaskColor uses color information to detect the board more accurately
func createBoardMaskColor(img image.Image, width, height int) [][]bool {
	bounds := img.Bounds()
	mask := make([][]bool, height)
	for y := range height {
		mask[y] = make([]bool, width)
	}

	// The board has white squares (very bright, low saturation) and
	// green/teal squares (medium brightness, greenish hue)
	// The background is dark wood (low brightness, brownish)
	for y := range height {
		for x := range width {
			c := img.At(bounds.Min.X+x, bounds.Min.Y+y)
			r, g, b, _ := c.RGBA()
			r8, g8, b8 := int(r>>8), int(g>>8), int(b>>8)

			brightness := (r8 + g8 + b8) / 3

			// Detect white/light squares (high brightness, low saturation)
			maxC := max(r8, max(g8, b8))
			minC := min(r8, min(g8, b8))
			saturation := 0
			if maxC > 0 {
				saturation = 100 * (maxC - minC) / maxC
			}

			isLightSquare := brightness > 160 && saturation < 30

			// Detect green/teal squares (medium brightness, green dominant)
			isGreenSquare := brightness > 80 && brightness < 160 &&
				g8 > r8 && g8 > b8-20 && // green is dominant or close to blue
				g8 > 60

			mask[y][x] = isLightSquare || isGreenSquare
		}
	}

	// Clean up with morphological operations
	mask = erodeMask(mask, width, height, 2)
	mask = dilateMask(mask, width, height, 2)

	// Keep only the largest connected component
	mask = keepLargestComponent(mask, width, height)

	return mask
}

// findCheckerboardStart moves corners inward until we find the checkerboard pattern
func findCheckerboardStart(corners []image.Point, img image.Image, gray [][]int, width, height int) []image.Point {
	if len(corners) != 4 {
		return corners
	}

	// Find center
	cx, cy := 0, 0
	for _, c := range corners {
		cx += c.X
		cy += c.Y
	}
	cx /= 4
	cy /= 4

	refined := make([]image.Point, 4)

	for i, corner := range corners {
		// Direction toward center
		dx := cx - corner.X
		dy := cy - corner.Y

		// Normalize
		stepX, stepY := 0, 0
		if dx > 0 {
			stepX = 1
		} else if dx < 0 {
			stepX = -1
		}
		if dy > 0 {
			stepY = 1
		} else if dy < 0 {
			stepY = -1
		}

		// Check if this board has a white border by scanning along the board edge
		// Skip white border detection for bottom-right corner (i=2, stepX<0 && stepY<0)
		// because the Y scan doesn't work well there due to the narrow white border
		startedOnWhiteBorder := false
		isBottomRight := stepX < 0 && stepY < 0

		// Only use white border detection for top corners
		isTopCorner := stepY > 0
		if !isBottomRight && isTopCorner {
			// For top corners, check if there's a white border extending to the top
			var boardX int
			centerY := height / 2
			if stepX > 0 {
				// Left corner - scan from left to find board
				for x := 0; x < width/2; x++ {
					if gray[centerY][x] > 150 {
						boardX = x + 20
						break
					}
				}
			} else {
				// Right corner - scan from right to find board
				for x := width - 1; x > width/2; x-- {
					if gray[centerY][x] > 150 {
						boardX = x - 20
						break
					}
				}
			}

			var edgeY int
			// Check if board has white border extending to top
			if gray[3][boardX] > 150 {
				// White border extends to top - use Y=3 for detection
				edgeY = 3
			} else {
				// No white border at top - skip white border detection
				edgeY = -1
			}

			// Skip white border detection if no white border at top
			if edgeY < 0 {
				// No white border - skip to non-white-border approach
			} else {
				// Scan from image edge toward center at this Y level
				var scanStart, scanDir int
				if stepX > 0 {
					// Left corner - scan from left edge
					scanStart = 0
					scanDir = 1
				} else {
					// Right corner - scan from right edge
					scanStart = width - 1
					scanDir = -1
				}

				// Look for pattern: dark -> bright (white border) -> less bright (checkerboard)
				// Require a bright streak (>15 pixels) to distinguish white border from noise
				foundDark := false
				brightStreak := 0
				for step := 0; step < width/2; step++ {
					x := scanStart + scanDir*step
					if x < 0 || x >= width {
						break
					}
					brightness := gray[edgeY][x]
					if !foundDark && brightness < 80 {
						foundDark = true
					} else if foundDark && brightness > 145 {
						brightStreak++
						if brightStreak >= 15 {
							startedOnWhiteBorder = true
							break
						}
					} else if foundDark && brightStreak > 0 && brightness < 130 {
						if brightStreak >= 15 {
							startedOnWhiteBorder = true
							break
						}
						// Streak too short - likely noise
						brightStreak = 0
					}
				}
			}
		}

		if startedOnWhiteBorder {
			// For white-bordered boards, scan from image edges to find actual board boundaries
			// Note: -stepX, -stepY indicate the corner direction
			candidate := findWhiteBorderCornerFromEdge(gray, -stepX, -stepY, width, height)

			// Validate: the candidate should not be at the WRONG edge.
			// For top-left corner (stepX>0, stepY>0): X can be near left edge, Y can be near top edge
			// But X should not be near right edge, Y should not be near bottom edge.
			edgeMargin := 20
			validCandidate := true

			// Check X is not at the wrong edge
			if stepX > 0 && candidate.X > width-edgeMargin {
				validCandidate = false // Left corner at right edge
			}
			if stepX < 0 && candidate.X < edgeMargin {
				validCandidate = false // Right corner at left edge
			}

			// Check Y is not at the wrong edge
			if stepY > 0 && candidate.Y > height-edgeMargin {
				validCandidate = false // Top corner at bottom edge
			}
			if stepY < 0 && candidate.Y < edgeMargin {
				validCandidate = false // Bottom corner at top edge
			}

			// Also validate: the candidate should be reasonably close to the initial corner
			// (within ~200 pixels in each dimension)
			dx := abs(candidate.X - corner.X)
			dy := abs(candidate.Y - corner.Y)
			if validCandidate && dx < 200 && dy < 200 {
				refined[i] = candidate
				continue
			}
			// Fall through to non-white-border approach
		}

		// Move inward until we detect a brightness transition (checkerboard edge)
		x, y := corner.X, corner.Y
		foundEdge := false

		for step := 0; step < 80 && !foundEdge; step++ {
			nx := x + stepX
			ny := y + stepY

			if nx < 1 || nx >= width-1 || ny < 1 || ny >= height-1 {
				break
			}

			// Check for brightness transition (edge of a square)
			var grad int
			if stepX != 0 && stepY == 0 {
				grad = abs(gray[ny+1][nx] - gray[ny-1][nx])
			} else if stepY != 0 && stepX == 0 {
				grad = abs(gray[ny][nx+1] - gray[ny][nx-1])
			} else {
				grad = abs(gray[ny+1][nx]-gray[ny-1][nx]) + abs(gray[ny][nx+1]-gray[ny][nx-1])
			}

			// Standard behavior for boards without white border
			if grad > 40 && step > 10 {
				foundEdge = true
			}

			x, y = nx, ny
		}

		// Fine-tune X and Y independently by scanning back toward boundary
		finalX := adjustCoordinate(gray, x, y, -stepX, 0, width, height, 20)
		finalY := adjustCoordinate(gray, x, y, 0, -stepY, width, height, 20)

		refined[i] = image.Point{finalX, finalY}
	}

	return refined
}

// findWhiteBorderCornerFromEdge finds the inner corner of the white border
// (where the white border meets the checkerboard) by scanning from the edges
func findWhiteBorderCornerFromEdge(gray [][]int, dirX, dirY, width, height int) image.Point {
	// Determine scan starting positions and directions
	var startX int
	var scanDirX int

	if dirX < 0 {
		startX = 0
		scanDirX = 1
	} else {
		startX = width - 1
		scanDirX = -1
	}

	// Find a good Y level for the X scan - needs to be where we can see
	// the transition from white border to coordinate labels/checkerboard
	var searchY int
	if dirY < 0 {
		// Top corner - use Y level where coordinate label text is visible
		// The coordinate labels (like "8") are typically around Y=8-12
		searchY = 10
	} else {
		// Bottom corner - use position near bottom where white border is visible
		searchY = height - 10
	}

	// Find X position: scan from edge, find white border, then find where it ends
	// (transition from white to checkerboard)
	finalX := startX
	foundWhite := false

	for step := 0; step < width; step++ {
		nx := startX + scanDirX*step
		if nx < 0 || nx >= width {
			break
		}
		brightness := gray[searchY][nx]

		if !foundWhite && brightness > 150 {
			// Found the white border
			foundWhite = true
		} else if foundWhite && brightness < 130 {
			// Found transition from white border to checkerboard
			// Back up to the last white position
			finalX = nx - scanDirX
			break
		}
	}

	// Find Y position: scan from edge at a position inside the white border
	// Move AWAY from the inner edge to be inside the white border
	searchX := finalX - scanDirX*15 // Move toward the center of the white border
	if searchX < 0 {
		searchX = 0
	} else if searchX >= width {
		searchX = width - 1
	}

	// Find Y position
	var finalY int
	if dirY < 0 {
		// Top corner - find where the white border starts from the top
		// Use a position inside the white border (not at the inner edge)
		searchXForY := finalX - scanDirX*30 // Move toward center of white border
		if searchXForY < 0 {
			searchXForY = 0
		} else if searchXForY >= width {
			searchXForY = width - 1
		}
		finalY = 0 // default
		for y := 0; y < height/4; y++ {
			if gray[y][searchXForY] > 150 {
				finalY = y
				break
			}
		}
	} else {
		// Bottom corner - find where the white border ends at the bottom
		foundWhiteBorder := false
		finalY = height - 1 // default to bottom
		for y := height - 1; y > height*3/4; y-- {
			brightness := gray[y][searchX]
			if brightness > 150 {
				foundWhiteBorder = true
			} else if foundWhiteBorder && brightness < 130 {
				finalY = y + 1
				break
			}
		}
	}

	return image.Point{finalX, finalY}
}

// adjustCoordinate scans in one direction to find the strongest edge
// Returns the adjusted X (if dx != 0) or Y (if dy != 0) coordinate
func adjustCoordinate(gray [][]int, startX, startY, dx, dy, width, height, maxSteps int) int {
	bestPos := startX
	if dy != 0 {
		bestPos = startY
	}
	bestGrad := 0

	// Scan in the given direction looking for a strong gradient
	for step := 0; step <= maxSteps; step++ {
		nx := startX + dx*step
		ny := startY + dy*step

		if nx < 2 || nx >= width-2 || ny < 2 || ny >= height-2 {
			break
		}

		// Compute gradient at this position
		grad := abs(gray[ny][nx+1]-gray[ny][nx-1]) + abs(gray[ny+1][nx]-gray[ny-1][nx])

		if grad > bestGrad {
			bestGrad = grad
			if dx != 0 {
				bestPos = nx
			} else {
				bestPos = ny
			}
		}
	}

	return bestPos
}

// findExtremeCorners finds the 4 corners with aspect ratio validation
func findExtremeCorners(points []image.Point) []image.Point {
	if len(points) < 4 {
		return points
	}

	// Get convex hull to filter out interior points
	hull := convexHull(points)
	if len(hull) < 4 {
		return findExtremePointsSimple(points)
	}

	// Find corners using extreme points method on the hull
	corners := findExtremePointsSimple(hull)

	// Validate: bottom should not be much wider than top
	topWidth := corners[1].X - corners[0].X
	bottomWidth := corners[2].X - corners[3].X

	// If bottom is more than 1.2x the top width, constrain based on top width
	if bottomWidth > topWidth*6/5 {
		bottomY := (corners[2].Y + corners[3].Y) / 2
		expectedLeftX := corners[0].X - (corners[0].Y-bottomY)/10
		expectedRightX := corners[1].X - (corners[1].Y-bottomY)/10

		corners[2] = findClosestHullPoint(hull, expectedRightX, bottomY)
		corners[3] = findClosestHullPoint(hull, expectedLeftX, bottomY)
	}

	return corners
}

func findClosestHullPoint(hull []image.Point, targetX, targetY int) image.Point {
	closest := hull[0]
	minDist := math.MaxFloat64

	for _, p := range hull {
		dx := float64(p.X - targetX)
		dy := float64(p.Y - targetY)
		dist := dx*dx + dy*dy
		if dist < minDist {
			minDist = dist
			closest = p
		}
	}

	return closest
}

func convexHull(points []image.Point) []image.Point {
	if len(points) < 3 {
		return points
	}

	sorted := make([]image.Point, len(points))
	copy(sorted, points)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].X != sorted[j].X {
			return sorted[i].X < sorted[j].X
		}
		return sorted[i].Y < sorted[j].Y
	})

	cross := func(o, a, b image.Point) int {
		return (a.X-o.X)*(b.Y-o.Y) - (a.Y-o.Y)*(b.X-o.X)
	}

	var lower []image.Point
	for _, p := range sorted {
		for len(lower) >= 2 && cross(lower[len(lower)-2], lower[len(lower)-1], p) <= 0 {
			lower = lower[:len(lower)-1]
		}
		lower = append(lower, p)
	}

	var upper []image.Point
	for i := len(sorted) - 1; i >= 0; i-- {
		p := sorted[i]
		for len(upper) >= 2 && cross(upper[len(upper)-2], upper[len(upper)-1], p) <= 0 {
			upper = upper[:len(upper)-1]
		}
		upper = append(upper, p)
	}

	return append(lower[:len(lower)-1], upper[:len(upper)-1]...)
}

func findExtremePointsSimple(points []image.Point) []image.Point {
	if len(points) == 0 {
		return nil
	}

	cx, cy := 0, 0
	for _, p := range points {
		cx += p.X
		cy += p.Y
	}
	cx /= len(points)
	cy /= len(points)

	var topLeft, topRight, bottomRight, bottomLeft image.Point
	var maxTL, maxTR, maxBR, maxBL int

	for _, p := range points {
		dx := p.X - cx
		dy := p.Y - cy

		if scoreTL := -dx - dy; scoreTL > maxTL {
			maxTL = scoreTL
			topLeft = p
		}
		if scoreTR := dx - dy; scoreTR > maxTR {
			maxTR = scoreTR
			topRight = p
		}
		if scoreBR := dx + dy; scoreBR > maxBR {
			maxBR = scoreBR
			bottomRight = p
		}
		if scoreBL := -dx + dy; scoreBL > maxBL {
			maxBL = scoreBL
			bottomLeft = p
		}
	}

	return []image.Point{topLeft, topRight, bottomRight, bottomLeft}
}

func makeGrayImage(img image.Image) [][]int {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	gray := make([][]int, height)
	for y := range height {
		gray[y] = make([]int, width)
		for x := range width {
			c := img.At(bounds.Min.X+x, bounds.Min.Y+y)
			r, g, b, _ := c.RGBA()
			gray[y][x] = (int(r>>8) + int(g>>8) + int(b>>8)) / 3
		}
	}
	return gray
}

// keepLargestComponent removes all but the largest connected component
func keepLargestComponent(mask [][]bool, width, height int) [][]bool {
	labels := make([][]int, height)
	for y := range height {
		labels[y] = make([]int, width)
	}

	componentSizes := make(map[int]int)
	currentLabel := 0

	for y := range height {
		for x := range width {
			if mask[y][x] && labels[y][x] == 0 {
				currentLabel++
				size := floodFill(mask, labels, x, y, width, height, currentLabel)
				componentSizes[currentLabel] = size
			}
		}
	}

	largestLabel := 0
	largestSize := 0
	for label, size := range componentSizes {
		if size > largestSize {
			largestSize = size
			largestLabel = label
		}
	}

	result := make([][]bool, height)
	for y := range height {
		result[y] = make([]bool, width)
		for x := range width {
			result[y][x] = labels[y][x] == largestLabel
		}
	}

	return result
}

func floodFill(mask [][]bool, labels [][]int, startX, startY, width, height, label int) int {
	stack := []image.Point{{startX, startY}}
	size := 0

	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		if p.X < 0 || p.X >= width || p.Y < 0 || p.Y >= height {
			continue
		}
		if !mask[p.Y][p.X] || labels[p.Y][p.X] != 0 {
			continue
		}

		labels[p.Y][p.X] = label
		size++

		stack = append(stack, image.Point{p.X + 1, p.Y})
		stack = append(stack, image.Point{p.X - 1, p.Y})
		stack = append(stack, image.Point{p.X, p.Y + 1})
		stack = append(stack, image.Point{p.X, p.Y - 1})
	}

	return size
}

func erodeMask(mask [][]bool, width, height, radius int) [][]bool {
	result := make([][]bool, height)
	for y := range height {
		result[y] = make([]bool, width)
	}

	for y := radius; y < height-radius; y++ {
		for x := radius; x < width-radius; x++ {
			allSet := true
			for dy := -radius; dy <= radius && allSet; dy++ {
				for dx := -radius; dx <= radius && allSet; dx++ {
					if !mask[y+dy][x+dx] {
						allSet = false
					}
				}
			}
			result[y][x] = allSet
		}
	}

	return result
}

func dilateMask(mask [][]bool, width, height, radius int) [][]bool {
	result := make([][]bool, height)
	for y := range height {
		result[y] = make([]bool, width)
	}

	for y := radius; y < height-radius; y++ {
		for x := radius; x < width-radius; x++ {
			anySet := false
			for dy := -radius; dy <= radius && !anySet; dy++ {
				for dx := -radius; dx <= radius && !anySet; dx++ {
					if mask[y+dy][x+dx] {
						anySet = true
					}
				}
			}
			result[y][x] = anySet
		}
	}

	return result
}

func findBoundary(mask [][]bool) []image.Point {
	height := len(mask)
	if height == 0 {
		return nil
	}
	width := len(mask[0])

	var boundary []image.Point

	for y := 1; y < height-1; y++ {
		for x := 1; x < width-1; x++ {
			if !mask[y][x] {
				continue
			}
			if !mask[y-1][x] || !mask[y+1][x] || !mask[y][x-1] || !mask[y][x+1] {
				boundary = append(boundary, image.Point{x, y})
			}
		}
	}

	return boundary
}

func defaultCorners(width, height int) []image.Point {
	return []image.Point{
		{width / 4, height / 8},
		{width * 3 / 4, height / 8},
		{width * 3 / 4, height * 7 / 8},
		{width / 4, height * 7 / 8},
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// Line represents a line in the form: rho = x*cos(theta) + y*sin(theta)
type Line struct {
	rho   float64
	theta float64
	votes int
}

// sobelEdgeDetection computes edge magnitude using Sobel operator
func sobelEdgeDetection(gray [][]int, width, height int) [][]int {
	edges := make([][]int, height)
	for y := range height {
		edges[y] = make([]int, width)
	}

	for y := 1; y < height-1; y++ {
		for x := 1; x < width-1; x++ {
			// Sobel X kernel
			gx := -gray[y-1][x-1] + gray[y-1][x+1] +
				-2*gray[y][x-1] + 2*gray[y][x+1] +
				-gray[y+1][x-1] + gray[y+1][x+1]

			// Sobel Y kernel
			gy := -gray[y-1][x-1] - 2*gray[y-1][x] - gray[y-1][x+1] +
				gray[y+1][x-1] + 2*gray[y+1][x] + gray[y+1][x+1]

			mag := int(math.Sqrt(float64(gx*gx + gy*gy)))
			if mag > 255 {
				mag = 255
			}
			edges[y][x] = mag
		}
	}

	return edges
}

// houghLineDetection detects lines using Hough transform
func houghLineDetection(edges [][]int, width, height int, edgeThreshold int) []Line {
	// Hough space parameters
	maxRho := int(math.Sqrt(float64(width*width + height*height)))
	numThetas := 180

	// Accumulator: rho ranges from -maxRho to +maxRho
	accumulator := make([][]int, 2*maxRho+1)
	for i := range accumulator {
		accumulator[i] = make([]int, numThetas)
	}

	// Pre-compute sin/cos values
	cosTheta := make([]float64, numThetas)
	sinTheta := make([]float64, numThetas)
	for t := 0; t < numThetas; t++ {
		theta := float64(t) * math.Pi / float64(numThetas)
		cosTheta[t] = math.Cos(theta)
		sinTheta[t] = math.Sin(theta)
	}

	// Vote for each edge pixel
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if edges[y][x] < edgeThreshold {
				continue
			}

			for t := 0; t < numThetas; t++ {
				rho := float64(x)*cosTheta[t] + float64(y)*sinTheta[t]
				rhoIdx := int(rho) + maxRho
				if rhoIdx >= 0 && rhoIdx < 2*maxRho+1 {
					accumulator[rhoIdx][t]++
				}
			}
		}
	}

	// Find peaks in accumulator (lines with many votes)
	var lines []Line
	voteThreshold := 100 // Minimum votes to consider a line

	for rhoIdx := 0; rhoIdx < 2*maxRho+1; rhoIdx++ {
		for t := 0; t < numThetas; t++ {
			if accumulator[rhoIdx][t] < voteThreshold {
				continue
			}

			// Local maximum check (simple 5x5 neighborhood)
			isMax := true
			for dr := -2; dr <= 2 && isMax; dr++ {
				for dt := -2; dt <= 2 && isMax; dt++ {
					if dr == 0 && dt == 0 {
						continue
					}
					nRho := rhoIdx + dr
					nT := (t + dt + numThetas) % numThetas
					if nRho >= 0 && nRho < 2*maxRho+1 {
						if accumulator[nRho][nT] > accumulator[rhoIdx][t] {
							isMax = false
						}
					}
				}
			}

			if isMax {
				rho := float64(rhoIdx - maxRho)
				theta := float64(t) * math.Pi / float64(numThetas)
				lines = append(lines, Line{rho: rho, theta: theta, votes: accumulator[rhoIdx][t]})
			}
		}
	}

	// Sort by votes (descending)
	sort.Slice(lines, func(i, j int) bool {
		return lines[i].votes > lines[j].votes
	})

	return lines
}

// lineIntersection finds the intersection point of two lines
func lineIntersection(l1, l2 Line) (image.Point, bool) {
	// Line 1: x*cos(t1) + y*sin(t1) = r1
	// Line 2: x*cos(t2) + y*sin(t2) = r2

	c1, s1 := math.Cos(l1.theta), math.Sin(l1.theta)
	c2, s2 := math.Cos(l2.theta), math.Sin(l2.theta)

	det := c1*s2 - c2*s1
	if math.Abs(det) < 1e-10 {
		return image.Point{}, false // Parallel lines
	}

	x := (s2*l1.rho - s1*l2.rho) / det
	y := (c1*l2.rho - c2*l1.rho) / det

	return image.Point{X: int(math.Round(x)), Y: int(math.Round(y))}, true
}

// findBoardLines finds the 4 lines forming the board border
func findBoardLines(lines []Line, corners []image.Point, width, height int) (topLine, bottomLine, leftLine, rightLine Line, found bool) {
	if len(lines) < 4 || len(corners) < 4 {
		return Line{}, Line{}, Line{}, Line{}, false
	}

	// Separate lines into horizontal (theta near 90 deg) and vertical (theta near 0 or 180 deg)
	var horizontalLines, verticalLines []Line
	for _, l := range lines {
		// theta is in radians, 0-pi
		// horizontal: theta near pi/2 (90 deg) - lines where y is mostly constant
		// vertical: theta near 0 or pi (0 or 180 deg) - lines where x is mostly constant
		if l.theta > math.Pi/4 && l.theta < 3*math.Pi/4 {
			horizontalLines = append(horizontalLines, l)
		} else {
			verticalLines = append(verticalLines, l)
		}
	}

	if len(horizontalLines) < 2 || len(verticalLines) < 2 {
		return Line{}, Line{}, Line{}, Line{}, false
	}

	// For horizontal lines: find the line that passes closest to the top corners
	// and the line that passes closest to the bottom corners
	topLine = findLineNearCorners(horizontalLines, []image.Point{corners[0], corners[1]})
	bottomLine = findLineNearCorners(horizontalLines, []image.Point{corners[2], corners[3]})

	// For vertical lines: find the line that passes closest to the left corners
	// and the line that passes closest to the right corners
	leftLine = findLineNearCorners(verticalLines, []image.Point{corners[0], corners[3]})
	rightLine = findLineNearCorners(verticalLines, []image.Point{corners[1], corners[2]})

	return topLine, bottomLine, leftLine, rightLine, true
}

// findLineNearCorners finds the line that passes closest to the given corners
// It prefers lines that are further from the center (outer border lines)
func findLineNearCorners(lines []Line, corners []image.Point) Line {
	best := lines[0]
	bestScore := math.MaxFloat64

	// Compute center of corners to determine which direction is "outer"
	centerX, centerY := 0.0, 0.0
	for _, c := range corners {
		centerX += float64(c.X)
		centerY += float64(c.Y)
	}
	centerX /= float64(len(corners))
	centerY /= float64(len(corners))

	for _, l := range lines {
		// Score = sum of distances from each corner to the line
		score := 0.0
		for _, c := range corners {
			dist := distanceToLine(l, c)
			score += dist
		}

		// Penalize lines that don't pass through the corner positions
		// But also prefer lines that are further from the board center
		avgCornerDist := score / float64(len(corners))

		// If the line passes reasonably close to corners (within 15 pixels average),
		// prefer the one furthest from center
		if avgCornerDist < 15 {
			// Compute line's distance from corner centroid
			// For outer lines, this should be larger
			lineDistFromCenter := math.Abs(centerX*math.Cos(l.theta) + centerY*math.Sin(l.theta) - l.rho)

			// Score: lower is better
			// Prefer lines close to corners but far from center
			adjustedScore := avgCornerDist - lineDistFromCenter/50.0
			if adjustedScore < bestScore {
				bestScore = adjustedScore
				best = l
			}
		} else {
			// Line is too far from corners, use basic scoring
			adjustedScore := avgCornerDist - float64(l.votes)/100.0
			if adjustedScore < bestScore {
				bestScore = adjustedScore
				best = l
			}
		}
	}

	return best
}

// distanceToLine computes perpendicular distance from point to line
func distanceToLine(l Line, pt image.Point) float64 {
	// Line: x*cos(theta) + y*sin(theta) = rho
	// Distance = |x*cos(theta) + y*sin(theta) - rho|
	return math.Abs(float64(pt.X)*math.Cos(l.theta) + float64(pt.Y)*math.Sin(l.theta) - l.rho)
}

// refineCornersWithLines refines corners using detected lines
func refineCornersWithLines(gray [][]int, corners []image.Point, width, height int) []image.Point {
	// Compute edges
	edges := sobelEdgeDetection(gray, width, height)

	// Detect lines
	lines := houghLineDetection(edges, width, height, 50)

	if len(lines) < 4 {
		return corners
	}

	// Find the 4 board border lines
	topLine, bottomLine, leftLine, rightLine, found := findBoardLines(lines, corners, width, height)
	if !found {
		return corners
	}

	// Compute intersections
	refined := make([]image.Point, 4)

	// Top-left: intersection of top and left lines
	if pt, ok := lineIntersection(topLine, leftLine); ok && isValidCorner(pt, width, height) {
		refined[0] = pt
	} else {
		refined[0] = corners[0]
	}

	// Top-right: intersection of top and right lines
	if pt, ok := lineIntersection(topLine, rightLine); ok && isValidCorner(pt, width, height) {
		refined[1] = pt
	} else {
		refined[1] = corners[1]
	}

	// Bottom-right: intersection of bottom and right lines
	if pt, ok := lineIntersection(bottomLine, rightLine); ok && isValidCorner(pt, width, height) {
		refined[2] = pt
	} else {
		refined[2] = corners[2]
	}

	// Bottom-left: intersection of bottom and left lines
	if pt, ok := lineIntersection(bottomLine, leftLine); ok && isValidCorner(pt, width, height) {
		refined[3] = pt
	} else {
		refined[3] = corners[3]
	}

	// Refine each corner by finding the exact edge positions
	refined = refineCornersByEdgeTracing(gray, refined, width, height)

	// For boards with white border, refine bottom corners by finding the actual border edge
	refined = refineWhiteBorderCorners(gray, refined, width, height)

	return refined
}

// refineCornersByEdgeTracing finds exact corner positions by looking for edge transitions
// very close to the already-detected corner positions
func refineCornersByEdgeTracing(gray [][]int, corners []image.Point, width, height int) []image.Point {
	refined := make([]image.Point, 4)
	copy(refined, corners)

	// For each corner, refine X and Y independently by finding the nearest strong edge
	// Corner order: 0=top-left, 1=top-right, 2=bottom-right, 3=bottom-left
	// Direction toward board center for each corner
	cornerDirs := [][2]int{
		{1, 1},   // top-left: board is to the right and down
		{-1, 1},  // top-right: board is to the left and down
		{-1, -1}, // bottom-right: board is to the left and up
		{1, -1},  // bottom-left: board is to the right and up
	}

	for i, corner := range corners {
		dirX, dirY := cornerDirs[i][0], cornerDirs[i][1]

		// Refine X: scan a small window around corner.X at a Y position inside the board
		// Look for the transition between board and non-board
		sampleY := corner.Y + dirY*15 // Sample 15 pixels inside the board
		if sampleY >= 2 && sampleY < height-2 {
			refinedX := findNearestEdgeX(gray, corner.X, sampleY, -dirX, width, 15)
			if refinedX >= 0 {
				refined[i].X = refinedX
			}
		}

		// Refine Y: scan a small window around corner.Y at an X position inside the board
		sampleX := corner.X + dirX*15 // Sample 15 pixels inside the board
		if sampleX >= 2 && sampleX < width-2 {
			refinedY := findNearestEdgeY(gray, sampleX, corner.Y, -dirY, height, 15)
			if refinedY >= 0 {
				refined[i].Y = refinedY
			}
		}
	}

	return refined
}

// findNearestEdgeX finds the X position of the nearest strong vertical edge
func findNearestEdgeX(gray [][]int, startX, y, searchDir, width, maxDist int) int {
	if y < 2 || y >= len(gray)-2 {
		return -1
	}

	bestX := startX
	bestGrad := 0

	for dx := -maxDist; dx <= maxDist; dx++ {
		x := startX + dx
		if x < 2 || x >= width-2 {
			continue
		}

		// Gradient in X direction (detecting vertical edges)
		grad := abs(gray[y][x+1] - gray[y][x-1])

		if grad > bestGrad {
			bestGrad = grad
			bestX = x
		}
	}

	// Only return if we found a significant edge
	if bestGrad > 40 {
		return bestX
	}
	return startX
}

// findNearestEdgeY finds the Y position of the nearest strong horizontal edge
func findNearestEdgeY(gray [][]int, x, startY, searchDir, height, maxDist int) int {
	if x < 2 || x >= len(gray[0])-2 {
		return -1
	}

	bestY := startY
	bestGrad := 0

	for dy := -maxDist; dy <= maxDist; dy++ {
		y := startY + dy
		if y < 2 || y >= height-2 {
			continue
		}

		// Gradient in Y direction (detecting horizontal edges)
		grad := abs(gray[y+1][x] - gray[y-1][x])

		if grad > bestGrad {
			bestGrad = grad
			bestY = y
		}
	}

	// Only return if we found a significant edge
	if bestGrad > 40 {
		return bestY
	}
	return startY
}

// refineWhiteBorderCorners adjusts corners for boards with white border frames
func refineWhiteBorderCorners(gray [][]int, corners []image.Point, width, height int) []image.Point {
	// Check if this is a white-bordered board by looking for white border that extends to the image edge
	// For white-bordered boards, the white border should be visible from the corner all the way to the top of the image
	leftX := corners[0].X
	topY := corners[0].Y

	// For a white-bordered board (like board3/4), the top corners should have topY close to 0
	// and there should be bright white from topY to Y=0
	// For regular boards (like board1/2), topY is typically 50+ pixels from the top

	isWhiteBordered := false

	// White-bordered boards have their top edge very close to Y=0 (within 10 pixels)
	// and bright white along the left edge at the very top of the image
	if topY < 15 {
		// Check if there's a bright white strip at the very top of the image
		// Sample at a position inside the left edge of the detected board
		sampleX := leftX + 10
		if sampleX < width {
			// Check if the first few rows (Y=0 to Y=5) are bright - this indicates
			// a white border that extends to the top of the image
			brightCount := 0
			for y := 0; y < 6 && y < height; y++ {
				if gray[y][sampleX] > 150 {
					brightCount++
				}
			}
			// If most of the top rows are bright, it's a white-bordered board
			if brightCount >= 4 {
				isWhiteBordered = true
			}
		}
	}

	if !isWhiteBordered {
		return corners // Not a white-bordered board
	}

	// For bottom corners, find where the white border actually ends
	// Sample at multiple X positions and use the maximum Y found
	// This accounts for perspective distortion where the board edge isn't horizontal
	bottomLeftX := corners[3].X
	bottomRightX := corners[2].X

	maxBottomY := 0
	// Sample at several positions across the board
	for i := 0; i < 5; i++ {
		sampleX := bottomLeftX + 20 + i*(bottomRightX-bottomLeftX-40)/4
		if sampleX >= 0 && sampleX < width {
			y := findWhiteBorderEdgeFromBottom(gray, sampleX, height)
			if y > maxBottomY {
				maxBottomY = y
			}
		}
	}

	// Update both bottom corners to use the maximum Y value found
	if maxBottomY > corners[2].Y && maxBottomY < height-5 {
		corners[2] = image.Point{corners[2].X, maxBottomY}
	}
	if maxBottomY > corners[3].Y && maxBottomY < height-5 {
		corners[3] = image.Point{corners[3].X, maxBottomY}
	}

	return corners
}

// findWhiteBorderEdgeFromBottom scans from the bottom of the image upward
// to find where the white border transitions to dark (the inner edge of the border)
func findWhiteBorderEdgeFromBottom(gray [][]int, x, height int) int {
	// Start from bottom, look for bright -> dark transition
	foundBright := false
	for y := height - 1; y > height/2; y-- {
		brightness := gray[y][x]
		if brightness > 150 {
			foundBright = true
		} else if foundBright && brightness < 100 {
			// Found the inner edge of the white border
			return y
		}
	}
	return height - 1
}

func isValidCorner(pt image.Point, width, height int) bool {
	margin := 50
	return pt.X >= -margin && pt.X < width+margin &&
		pt.Y >= -margin && pt.Y < height+margin
}
