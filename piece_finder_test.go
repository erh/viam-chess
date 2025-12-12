package viamchess

import (
	"testing"

	"go.viam.com/rdk/pointcloud"
	"go.viam.com/rdk/rimage"
	"go.viam.com/test"

	"github.com/erh/vmodutils/touch"
)

func TestPieceFinder1(t *testing.T) {
	input, err := rimage.ReadImageFromFile("data/hack1.jpg")
	test.That(t, err, test.ShouldBeNil)

	pc, err := pointcloud.NewFromFile("data/hack1.pcd", "")
	test.That(t, err, test.ShouldBeNil)

	out, _, err := BoardDebugImageHack(input, pc, touch.RealSenseProperties)
	test.That(t, err, test.ShouldBeNil)

	err = rimage.WriteImageToFile("hack-test.jpg", out)
	test.That(t, err, test.ShouldBeNil)

}
