package viamchess

import (
	"fmt"
	"strings"

	"github.com/golang/geo/r3"

	viz "go.viam.com/rdk/vision"
	"go.viam.com/rdk/vision/objectdetection"
	"go.viam.com/rdk/vision/viscapture"
)

func (s *viamChessChess) findObject(data viscapture.VisCapture, pos string) *viz.Object {
	for _, o := range data.Objects {
		if strings.HasPrefix(o.Geometry.Label(), pos) {
			return o
		}
	}
	return nil
}

func (s *viamChessChess) findDetection(data viscapture.VisCapture, pos string) objectdetection.Detection {
	for _, d := range data.Detections {
		if strings.HasPrefix(d.Label(), pos) {
			return d
		}
	}
	return nil
}

// isWhite=true → a-file side (negative Y); false → h-file side (positive Y).
func (s *viamChessChess) graveyardPosition(data viscapture.VisCapture, colorIdx int, isWhite bool) (r3.Vector, error) {
	ex := 1 + (colorIdx / 8)

	var k string
	if isWhite {
		k = fmt.Sprintf("a%d", 8-(colorIdx%8))
	} else {
		k = fmt.Sprintf("h%d", 1+(colorIdx%8))
	}

	oo := s.findObject(data, k)
	if oo == nil {
		return r3.Vector{}, fmt.Errorf("why no object for %s", k)
	}

	md := oo.MetaData()
	if isWhite {
		return r3.Vector{md.Center().X, md.Center().Y - float64(ex*80), 60}, nil
	}
	return r3.Vector{md.Center().X, md.Center().Y + float64(ex*80), 60}, nil
}

func (s *viamChessChess) getCenterFor(data viscapture.VisCapture, pos string, theState *state) (r3.Vector, error) {
	if pos == "-" {
		// Fallback for hover/other callers; movePiece handles graveyard
		// placement directly with the captured piece's color.
		return r3.Vector{400, -400, 200}, nil
	}

	if pos[0] == 'X' {
		// "XW{n}" / "XB{n}" = white/black graveyard slot n.
		if len(pos) >= 3 {
			x := -1
			if pos[1] == 'W' {
				fmt.Sscanf(pos, "XW%d", &x)
				return s.graveyardPosition(data, x, true)
			}
			if pos[1] == 'B' {
				fmt.Sscanf(pos, "XB%d", &x)
				return s.graveyardPosition(data, x, false)
			}
		}
		return r3.Vector{}, fmt.Errorf("bad special graveyard (%s)", pos)
	}

	o := s.findObject(data, pos)
	if o == nil {
		return r3.Vector{}, fmt.Errorf("can't find object for: %s", pos)
	}

	return GetPickupCenter(o), nil
}
