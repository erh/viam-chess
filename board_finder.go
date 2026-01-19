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

		if !isBottomRight {
			// For this corner, determine the edge to scan
			var edgeY int
			if stepY > 0 {
				// Top corner - scan near top edge
				edgeY = 20
			} else {
				// Bottom corner - scan near bottom edge
				edgeY = height - 20
			}

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
				} else if foundDark && brightness > 170 {
					brightStreak++
					if brightStreak >= 15 {
						startedOnWhiteBorder = true
						break
					}
				} else if foundDark && brightStreak > 0 && brightness < 150 {
					if brightStreak >= 15 {
						startedOnWhiteBorder = true
						break
					}
					// Streak too short - was likely coordinate labels
					brightStreak = 0
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

// findOuterBoardEdge scans OUTWARD from a position to find where the board meets dark background
// Returns the X (if dx != 0) or Y (if dy != 0) coordinate at the board's outer edge
// findWhiteBorderCornerFromEdge finds the inner edge of the white border
// (where the checkerboard pattern starts) by:
// 1. Scanning from image edge to find outer edge of white border
// 2. Continuing to find where brightness drops (inner edge = checkerboard start)
func findWhiteBorderCornerFromEdge(gray [][]int, dirX, dirY, width, height int) image.Point {
	// Determine scan directions and edge positions
	var scanDirX, scanDirY int
	var startX, startY int

	if dirX < 0 {
		startX = 0          // start at left edge
		scanDirX = 1        // scan toward right (into board)
	} else {
		startX = width - 1  // start at right edge
		scanDirX = -1       // scan toward left (into board)
	}
	if dirY < 0 {
		startY = 0          // start at top edge
		scanDirY = 1        // scan toward bottom (into board)
	} else {
		startY = height - 1 // start at bottom edge
		scanDirY = -1       // scan toward top (into board)
	}

	// Find X position of inner edge (white border -> checkerboard transition)
	// For top corners, scan at Y close to top edge (Y=8)
	// For bottom corners, scan further inside where checkerboard is visible
	var searchY int
	if dirY < 0 {
		// Top corner - use Y very close to top edge
		searchY = 8
	} else {
		// Bottom corner - scan at a Y level where we can see the checkerboard
		// The inner edge of white border is ~20-30 pixels from the image edge
		// So scan at Y = height - 40 to be inside the checkerboard area
		searchY = height - 40
	}

	finalX := startX
	foundWhite := false

	for step := 0; step < width; step++ {
		nx := startX + scanDirX*step
		if nx < 0 || nx >= width {
			break
		}
		brightness := gray[searchY][nx]

		if !foundWhite && brightness > 170 {
			// Found the white border (threshold lowered for bottom edge)
			foundWhite = true
		} else if foundWhite && brightness < 120 {
			// Found transition to checkerboard
			// The inner edge is at the last white position
			finalX = nx - scanDirX
			break
		}
	}

	// Find Y position of the board edge
	// For the Y scan, use the found X. finalX is at the inner edge of the white
	// border, so we scan at that position or slightly into the checkerboard area
	// to ensure we find the vertical transition.
	searchX := finalX + scanDirX*3 // Move 3 pixels into the checkerboard area
	if searchX < 0 {
		searchX = 0
	} else if searchX >= width {
		searchX = width - 1
	}

	finalY := startY
	foundWhiteBorder := false

	for step := 0; step < height; step++ {
		ny := startY + scanDirY*step
		if ny < 0 || ny >= height {
			break
		}
		brightness := gray[ny][searchX]

		// Looking for: [dark background?] → white border → checkerboard
		// The inner edge is where white border (>180) transitions to checkerboard (<140)
		if brightness > 180 {
			foundWhiteBorder = true
		} else if foundWhiteBorder && brightness < 140 {
			// Found the inner edge (white border → checkerboard transition)
			// Back up one step to be on the last white border pixel
			finalY = ny - scanDirY
			break
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
