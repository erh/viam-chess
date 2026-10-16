package viamchess

import (
	"context"
	"fmt"
	"time"

	"github.com/corentings/chess/v2"

	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/services/generic"
	"go.viam.com/rdk/services/task"
)

// Task models. Each is a unit of chess work with an outcome; the state
// machine service wires them into modes (see examples/config1.json).
var (
	// WatchBoardModel keeps the board snapshot fresh and never returns.
	WatchBoardModel = family.WithModel("watch-board")
	// WaitHumanMoveModel watches until the human has moved: outcomes
	// "moved", "engine_turn" and "game_over".
	WaitHumanMoveModel = family.WithModel("wait-human-move")
	// EngineMoveModel plays one engine move on the physical board: outcomes
	// "done" and "game_over".
	EngineMoveModel = family.WithModel("engine-move")
	// HomeModel homes the arm and opens the gripper: outcome "done".
	HomeModel = family.WithModel("home")
	// ResetBoardModel physically returns every piece to its home square:
	// outcome "done".
	ResetBoardModel = family.WithModel("reset-board")
)

// Task outcomes beyond the standard "done".
const (
	OutcomeMoved      = "moved"
	OutcomeEngineTurn = "engine_turn"
	OutcomeGameOver   = "game_over"
)

// TaskConfig configures every chess task.
type TaskConfig struct {
	// Chess is the name of the chess service the task drives.
	Chess string `json:"chess"`
	// IntervalMs is how often a watching task looks at the board. Default 1000.
	IntervalMs int `json:"interval-ms,omitempty"`
}

// Validate validates the config and declares the chess service dependency.
func (cfg *TaskConfig) Validate(path string) ([]string, []string, error) {
	if cfg.Chess == "" {
		return nil, nil, resource.NewConfigValidationFieldRequiredError(path, "chess")
	}
	return []string{generic.Named(cfg.Chess).String()}, nil, nil
}

func (cfg *TaskConfig) interval() time.Duration {
	if cfg.IntervalMs <= 0 {
		return time.Second
	}
	return time.Duration(cfg.IntervalMs) * time.Millisecond
}

type taskBody func(t *chessTask, ctx context.Context, input map[string]interface{}, report task.ProgressFunc) (task.Result, error)

func init() {
	for model, body := range map[resource.Model]taskBody{
		WatchBoardModel:    (*chessTask).watchBoard,
		WaitHumanMoveModel: (*chessTask).waitHumanMove,
		EngineMoveModel:    (*chessTask).engineMove,
		HomeModel:          (*chessTask).home,
		ResetBoardModel:    (*chessTask).resetBoard,
	} {
		resource.RegisterService(task.API, model, resource.Registration[task.Service, *TaskConfig]{
			Constructor: func(
				ctx context.Context, deps resource.Dependencies, conf resource.Config, logger logging.Logger,
			) (task.Service, error) {
				return newChessTask(deps, conf, logger, body)
			},
		})
	}
}

// chessTask is a task bound to a chess service in the same module.
type chessTask struct {
	resource.Named
	*task.Runner
	logger logging.Logger
	conf   *TaskConfig
	chess  *viamChessChess
}

func newChessTask(deps resource.Dependencies, conf resource.Config, logger logging.Logger, body taskBody) (task.Service, error) {
	cfg, err := resource.NativeConfig[*TaskConfig](conf)
	if err != nil {
		return nil, err
	}
	res, err := generic.FromProvider(deps, cfg.Chess)
	if err != nil {
		return nil, err
	}
	chessSvc, ok := res.(*viamChessChess)
	if !ok {
		return nil, fmt.Errorf("chess %q must be an %s service in this module, got %T", cfg.Chess, ChessModel, res)
	}
	t := &chessTask{Named: conf.ResourceName().AsNamed(), logger: logger, conf: cfg, chess: chessSvc}
	t.Runner = task.NewRunner(func(ctx context.Context, input map[string]interface{}, report task.ProgressFunc) (task.Result, error) {
		return body(t, ctx, input, report)
	})
	return t, nil
}

// Close cancels any execution in flight.
func (t *chessTask) Close(ctx context.Context) error {
	t.Cancel()
	return nil
}

func (t *chessTask) sleep(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(t.conf.interval()):
		return nil
	}
}

// watchBoard is the passive loop: home the arm so the camera is clear,
// capture, refresh the snapshot, repeat until cancelled. It never moves a
// piece. A failed home or capture is transient: log it and try again.
func (t *chessTask) watchBoard(ctx context.Context, input map[string]interface{}, report task.ProgressFunc) (task.Result, error) {
	for {
		t.chess.doCommandLock.Lock()
		all, err := t.chess.scanBoard(ctx)
		if err == nil {
			t.chess.setNeedsFix(false, "")
			if err := t.chess.refreshBoardCache(ctx, all); err != nil {
				t.logger.Warnf("watch-board: cache refresh failed: %v", err)
			}
		} else if ctx.Err() == nil {
			t.logger.Warnf("watch-board: scan failed (tick lost): %v", err)
		}
		t.chess.doCommandLock.Unlock()
		report(task.Progress{Message: "watching the board"})
		if err := t.sleep(ctx); err != nil {
			return task.Result{}, err
		}
	}
}

// waitHumanMove watches the board until the human's move is registered. It
// returns "engine_turn" at once when it is the robot's turn (so the machine
// can reply) and "game_over" when the game has ended. A board that cannot be
// reconciled with the game sets needs_fix and keeps waiting.
func (t *chessTask) waitHumanMove(ctx context.Context, input map[string]interface{}, report task.ProgressFunc) (task.Result, error) {
	side := robotSide(input)
	for {
		t.chess.doCommandLock.Lock()
		res, done, err := t.detectHumanMove(ctx, side)
		t.chess.doCommandLock.Unlock()
		if err != nil {
			return task.Result{}, err
		}
		if done {
			return res, nil
		}
		report(task.Progress{Message: "waiting for the human to move"})
		if err := t.sleep(ctx); err != nil {
			return task.Result{}, err
		}
	}
}

func (t *chessTask) detectHumanMove(ctx context.Context, side chess.Color) (task.Result, bool, error) {
	theState, err := t.chess.getGame(ctx)
	if err != nil {
		return task.Result{}, false, err
	}
	if theState.game.Outcome() != chess.NoOutcome {
		return gameResult(OutcomeGameOver, theState, nil), true, nil
	}
	if theState.game.Position().Turn() == side {
		return gameResult(OutcomeEngineTurn, theState, nil), true, nil
	}

	all, err := t.chess.scanBoard(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return task.Result{}, false, err
		}
		t.logger.Warnf("wait-human-move: scan failed (tick lost): %v", err)
		return task.Result{}, false, nil
	}
	m, err := t.chess.checkPositionForMoves(ctx, all)
	if err != nil {
		// The board can't be mapped to a legal move (illegal/scrambled, or a
		// transient capture hiccup): surface needs_fix and wait for a human.
		t.chess.setNeedsFix(true, err.Error())
	} else {
		t.chess.setNeedsFix(false, "")
	}
	if err := t.chess.refreshBoardCache(ctx, all); err != nil {
		t.logger.Warnf("wait-human-move: cache refresh failed: %v", err)
	}
	if m == nil {
		return task.Result{}, false, nil
	}
	t.logger.Infof("detected human move %s", m.String())
	theState, err = t.chess.getGame(ctx)
	if err != nil {
		return task.Result{}, false, err
	}
	if theState.game.Outcome() != chess.NoOutcome {
		return gameResult(OutcomeGameOver, theState, m), true, nil
	}
	return gameResult(OutcomeMoved, theState, m), true, nil
}

// engineMove plays one engine move on the physical board. Input:
//
//	side: "white" or "black" to move only when it is that side's turn
//	      (otherwise "done" with moved=false); omit to move for whoever is on turn.
//	sanity_check: true to verify the board against the game before moving.
//	      The first move of a game is always checked.
//
// A board that does not match the game sets needs_fix and waits for a human
// to fix it; an execution fault (arm, gripper, engine) is an error. The
// physical move always completes even if the task is cancelled, so a pause
// never drops a piece.
func (t *chessTask) engineMove(ctx context.Context, input map[string]interface{}, report task.ProgressFunc) (task.Result, error) {
	side := robotSide(input)
	sanity, _ := input["sanity_check"].(bool)
	for {
		t.chess.doCommandLock.Lock()
		res, done, err := t.tryEngineMove(ctx, side, sanity, report)
		t.chess.doCommandLock.Unlock()
		if err != nil {
			return task.Result{}, err
		}
		if done {
			return res, nil
		}
		report(task.Progress{Message: "waiting for the board to be set up"})
		if err := t.sleep(ctx); err != nil {
			return task.Result{}, err
		}
	}
}

func (t *chessTask) tryEngineMove(
	ctx context.Context, side chess.Color, sanity bool, report task.ProgressFunc,
) (task.Result, bool, error) {
	theState, err := t.chess.getGame(ctx)
	if err != nil {
		return task.Result{}, false, err
	}
	if theState.game.Outcome() != chess.NoOutcome {
		return gameResult(OutcomeGameOver, theState, nil), true, nil
	}
	if side != chess.NoColor && theState.game.Position().Turn() != side {
		res := gameResult(task.OutcomeDone, theState, nil)
		res.Output["moved"] = false
		return res, true, nil
	}
	if err := ctx.Err(); err != nil {
		return task.Result{}, false, err
	}

	report(task.Progress{Message: "playing a move"})
	firstPly := len(theState.game.Moves()) == 0
	// Detached: a cancellation (pause, mode switch) lets the in-flight move
	// finish; aborting mid-reach would drop a piece.
	m, err := t.chess.makeAMove(context.WithoutCancel(ctx), sanity || firstPly)
	if err != nil {
		if isExecFailure(err) {
			return task.Result{}, false, err
		}
		// Non-exec: the board isn't set up as the game expects. Wait and prompt.
		t.chess.setNeedsFix(true, err.Error())
		return task.Result{}, false, nil
	}
	t.chess.setNeedsFix(false, "")
	if all, err := t.chess.scanBoard(ctx); err == nil {
		_ = t.chess.refreshBoardCache(ctx, all)
	}
	theState, err = t.chess.getGame(ctx)
	if err != nil {
		return task.Result{}, false, err
	}
	outcome := task.OutcomeDone
	if theState.game.Outcome() != chess.NoOutcome {
		outcome = OutcomeGameOver
	}
	res := gameResult(outcome, theState, m)
	res.Output["moved"] = true
	return res, true, nil
}

// home homes the arm and opens the gripper. Used by the error state so the
// arm is parked safely after a fault.
func (t *chessTask) home(ctx context.Context, input map[string]interface{}, report task.ProgressFunc) (task.Result, error) {
	t.chess.doCommandLock.Lock()
	defer t.chess.doCommandLock.Unlock()
	if err := t.chess.goToStart(ctx); err != nil {
		return task.Result{}, err
	}
	return task.Done(nil), nil
}

// resetBoard physically returns every piece to its home square and wipes the
// game. Cancellation takes effect between piece moves.
func (t *chessTask) resetBoard(ctx context.Context, input map[string]interface{}, report task.ProgressFunc) (task.Result, error) {
	t.chess.doCommandLock.Lock()
	defer t.chess.doCommandLock.Unlock()
	report(task.Progress{Message: "resetting the board"})
	if err := t.chess.resetBoard(ctx); err != nil {
		return task.Result{}, err
	}
	t.chess.invalidateBoardCache()
	return task.Done(nil), nil
}

// robotSide reads the optional "side" input.
func robotSide(input map[string]interface{}) chess.Color {
	switch input["side"] {
	case "white":
		return chess.White
	case "black":
		return chess.Black
	default:
		return chess.NoColor
	}
}

// gameResult builds a task result carrying the game's state.
func gameResult(outcome string, theState *state, m *chess.Move) task.Result {
	events := gameEventsResult(theState.game)
	output := map[string]interface{}{
		"fen":     theState.game.FEN(),
		"turn":    events.Turn,
		"outcome": events.Outcome,
		"method":  events.Method,
		"is_over": events.IsOver,
	}
	if m != nil {
		output["move"] = m.String()
	}
	return task.Result{Outcome: outcome, Output: output}
}
