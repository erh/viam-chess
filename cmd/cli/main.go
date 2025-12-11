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
	
	flag.Parse()

	if *debug {
		logger.SetLevel(logging.DEBUG)
	}

	if *host == "" {
		return fmt.Errorf("need a host")
	}
	
	cfg := viamchess.ChessConfig{}
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

	return nil
}
