package main

import (
	"viamchess"
	"go.viam.com/rdk/module"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/components/camera"
	generic "go.viam.com/rdk/services/generic"
)

func main() {

	module.ModularMain(resource.APIModel{ camera.API, viamchess.BoardCameraModel})
	module.ModularMain(resource.APIModel{ generic.API, viamchess.ChessModel})
}
