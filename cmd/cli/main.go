package main

import (
	"context"
	"flag"
	"fmt"

	"go.viam.com/rdk/logging"
	generic "go.viam.com/rdk/services/generic"

	"github.com/erh/vmodutils"

	"viamchess"
)

func main() {
	err := realMain()
	if err != nil {
		panic(err)
	}
}

func realMain() error {
	ctx := context.Background()
	logger := logging.NewLogger("cli")

	host := flag.String("host", "", "host")
	debug := flag.Bool("debug", false, "")
	cmd := flag.String("cmd", "", "host")

	from := flag.String("from", "", "")
	to := flag.String("to", "", "")
	n := flag.Int("n", 1, "")

	flag.Parse()

	if *debug {
		logger.SetLevel(logging.DEBUG)
	}

	if *host == "" {
		return fmt.Errorf("need a host")
	}

	if *cmd == "" {
		return fmt.Errorf("need command")
	}

	cfg := viamchess.ChessConfig{
		PieceFinder: "piece-finder",
		Arm:         "arm",
		Gripper:     "gripper",
		PoseStart:   "hack-pose-look-straight-down",
	}
	_, _, err := cfg.Validate("")
	if err != nil {
		return err
	}

	machine, err := vmodutils.ConnectToHostFromCLIToken(ctx, *host, logger)
	if err != nil {
		return err
	}
	defer machine.Close(ctx)

	deps, err := vmodutils.MachineToDependencies(machine)
	if err != nil {
		return err
	}

	thing, err := viamchess.NewChess(ctx, deps, generic.Named("foo"), &cfg, logger)
	if err != nil {
		return err
	}
	defer thing.Close(ctx)

	switch *cmd {
	case "move":
		res, err := thing.DoCommand(ctx, map[string]interface{}{
			"move": map[string]interface{}{"from": *from, "to": *to, "n": *n},
		})
		if err != nil {
			return err
		}
		logger.Infof("res: %v", res)
		return nil
	case "go":
		res, err := thing.DoCommand(ctx, map[string]interface{}{
			"go": *n,
		})
		if err != nil {
			return err
		}
		logger.Infof("res: %v", res)
		return nil

	default:
		return fmt.Errorf("unknown command [%s]", *cmd)
	}

	return nil
}
