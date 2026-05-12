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

func (s *viamChessChess) graveyardPosition(data viscapture.VisCapture, pos int) (r3.Vector, error) {
	f := 8 - (pos % 8)
	ex := 1 + (pos / 8)

	k := fmt.Sprintf("a%d", f)
	oo := s.findObject(data, k)
	if oo == nil {
		return r3.Vector{}, fmt.Errorf("why no object for %s", k)
	}

	md := oo.MetaData()
	return r3.Vector{md.Center().X, md.Center().Y - float64(ex*80), 60}, nil

}

func (s *viamChessChess) getCenterFor(data viscapture.VisCapture, pos string, theState *state) (r3.Vector, error) {
	if pos == "-" {
		if s == nil {
			return r3.Vector{400, -400, 200}, nil
		}
		return s.graveyardPosition(data, len(theState.graveyard))
	}

	if pos[0] == 'X' {
		x := -1
		_, err := fmt.Sscanf(pos, "X%d", &x)
		if err != nil {
			return r3.Vector{}, fmt.Errorf("bad special graveyard (%s)", pos)
		}

		return s.graveyardPosition(data, x)
	}

	o := s.findObject(data, pos)
	if o == nil {
		return r3.Vector{}, fmt.Errorf("can't find object for: %s", pos)
	}

	return GetPickupCenter(o), nil
}
