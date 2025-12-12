package main

import (
	"go.viam.com/rdk/module"
	"go.viam.com/rdk/resource"
	generic "go.viam.com/rdk/services/generic"
	"go.viam.com/rdk/services/vision"
	"viamchess"
)

func main() {

	module.ModularMain(resource.APIModel{vision.API, viamchess.PieceFinderModel})
	module.ModularMain(resource.APIModel{generic.API, viamchess.ChessModel})
}
