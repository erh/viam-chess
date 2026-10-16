package main

import (
	"go.viam.com/rdk/module"
	"go.viam.com/rdk/resource"
	generic "go.viam.com/rdk/services/generic"
	"go.viam.com/rdk/services/task"
	"go.viam.com/rdk/services/vision"
	"viamchess"
)

func main() {
	module.ModularMain(
		resource.APIModel{API: vision.API, Model: viamchess.PieceFinderModel},
		resource.APIModel{API: generic.API, Model: viamchess.ChessModel},
		resource.APIModel{API: task.API, Model: viamchess.WatchBoardModel},
		resource.APIModel{API: task.API, Model: viamchess.WaitHumanMoveModel},
		resource.APIModel{API: task.API, Model: viamchess.EngineMoveModel},
		resource.APIModel{API: task.API, Model: viamchess.HomeModel},
		resource.APIModel{API: task.API, Model: viamchess.ResetBoardModel},
	)
}
