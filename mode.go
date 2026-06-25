package viamchess

import (
	"errors"
	"fmt"
	"sync"
)

// Mode is the robot's single source of truth for behavior. At any moment the
// machine is in exactly one mode; see STATE_MACHINE_SPEC.md for the authoritative
// design and transition table.
type Mode int

const (
	ModeStart    Mode = 0 // no active game; boot/entry hub, lands here after reset
	ModeIdle     Mode = 1 // active game suspended (paused) or finished (gameOver)
	ModeVsHuman  Mode = 2 // robot plays black, human plays white (today's "auto")
	ModeVsSelf   Mode = 3 // robot plays both sides, blind, one ply per tick
	ModeTeaching Mode = 4 // stub: transitions work, no behavior yet
	ModeError    Mode = 5 // entered on execution failure; halts loop, homes arm
)

func (m Mode) String() string {
	switch m {
	case ModeStart:
		return "START"
	case ModeIdle:
		return "IDLE"
	case ModeVsHuman:
		return "VS_HUMAN"
	case ModeVsSelf:
		return "VS_SELF"
	case ModeTeaching:
		return "TEACHING"
	case ModeError:
		return "ERROR"
	default:
		return fmt.Sprintf("Mode(%d)", int(m))
	}
}

// modeMachine holds the mode plus the small amount of correlated state the
// transition table needs. All access goes through its methods so transitions
// are atomic. idleOrigin and errPrevMode are kept separate (not one shared
// "prevMode") so a fault while paused (IDLE -> ERROR -> resume -> IDLE) still
// knows IDLE's own origin.
type modeMachine struct {
	mu          sync.RWMutex
	mode        Mode
	idleOrigin  Mode // where IDLE resumes to; set on entering IDLE
	errPrevMode Mode // where ERROR resumes to; set on entering ERROR
	gameOver    bool // set when a game ends into IDLE; disables resume
}

// the zero value is a valid START state with no game.

type modeSnapshot struct {
	Mode       Mode
	IdleOrigin Mode
	ErrPrev    Mode
	GameOver   bool
}

func (mm *modeMachine) current() Mode {
	mm.mu.RLock()
	defer mm.mu.RUnlock()
	return mm.mode
}

func (mm *modeMachine) snapshot() modeSnapshot {
	mm.mu.RLock()
	defer mm.mu.RUnlock()
	return modeSnapshot{
		Mode:       mm.mode,
		IdleOrigin: mm.idleOrigin,
		ErrPrev:    mm.errPrevMode,
		GameOver:   mm.gameOver,
	}
}

// transition attempts a command-driven edge to `to`, validating it against the
// authoritative table plus guards. It returns the prior mode (so the caller can
// run edge-specific side-effects, e.g. wiping state.json on START -> active) and
// an error if the edge is illegal (in which case the mode is unchanged).
//
// Automatic transitions (game-over -> IDLE, any -> ERROR) do NOT go through here;
// see enterIdle / enterError.
func (mm *modeMachine) transition(to Mode) (from Mode, err error) {
	mm.mu.Lock()
	defer mm.mu.Unlock()

	from = mm.mode
	switch from {
	case ModeStart:
		// Choose an opponent. (Assumes the board is already set up.)
		if to == ModeVsHuman || to == ModeVsSelf {
			mm.mode = to
			return from, nil
		}
	case ModeVsHuman:
		switch to {
		case ModeTeaching: // toggle teaching, game preserved
			mm.mode = ModeTeaching
			return from, nil
		case ModeIdle: // pause
			mm.idleOrigin = ModeVsHuman
			mm.gameOver = false
			mm.mode = ModeIdle
			return from, nil
		}
	case ModeVsSelf:
		if to == ModeIdle { // pause
			mm.idleOrigin = ModeVsSelf
			mm.gameOver = false
			mm.mode = ModeIdle
			return from, nil
		}
	case ModeTeaching:
		switch to {
		case ModeVsHuman: // back to game
			mm.mode = ModeVsHuman
			return from, nil
		case ModeIdle: // pause
			mm.idleOrigin = ModeTeaching
			mm.gameOver = false
			mm.mode = ModeIdle
			return from, nil
		}
	case ModeIdle:
		if to == ModeStart { // reset
			mm.mode = ModeStart
			mm.gameOver = false
			return from, nil
		}
		if to == mm.idleOrigin && !mm.gameOver { // resume (disabled once gameOver)
			mm.mode = to
			return from, nil
		}
	case ModeError:
		if to == ModeStart { // reset
			mm.mode = ModeStart
			mm.gameOver = false
			return from, nil
		}
		if to == mm.errPrevMode { // resume (caller verifies game is intact)
			mm.mode = to
			return from, nil
		}
	}

	return from, fmt.Errorf("illegal transition %v -> %v (idleOrigin=%v errPrev=%v gameOver=%v)",
		from, to, mm.idleOrigin, mm.errPrevMode, mm.gameOver)
}

// enterIdle is the automatic game-over (gameOver=true) path. It records the
// active mode as the origin and parks in IDLE. If the mode already changed to
// IDLE (a pause raced the final move) it only raises gameOver; if the game was
// abandoned (START/ERROR) it is a no-op.
func (mm *modeMachine) enterIdle(gameOver bool) {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	switch mm.mode {
	case ModeVsHuman, ModeVsSelf, ModeTeaching:
		mm.idleOrigin = mm.mode
		mm.gameOver = gameOver
		mm.mode = ModeIdle
	case ModeIdle:
		if gameOver {
			mm.gameOver = true
		}
	}
}

// enterError records the current mode and halts into ERROR. Called on any
// execution failure (see isExecFailure). Idempotent.
func (mm *modeMachine) enterError() {
	mm.mu.Lock()
	defer mm.mu.Unlock()
	if mm.mode == ModeError {
		return
	}
	mm.errPrevMode = mm.mode
	mm.mode = ModeError
}

// execError marks a failure as an execution fault (arm/gripper/engine) — the
// only kind that trips ERROR. Detection, vision, and invalid-input errors stay
// unwrapped and are handled benignly.
type execError struct{ err error }

func (e execError) Error() string { return e.err.Error() }
func (e execError) Unwrap() error { return e.err }

// errExec wraps an error as an execution fault. Apply at the lowest call site
// (arm/gripper/engine). nil stays nil.
func errExec(err error) error {
	if err == nil {
		return nil
	}
	return execError{err: err}
}

// isExecFailure reports whether err (or anything it wraps) is an execution
// fault, so the loop and manual command paths can decide ERROR uniformly.
func isExecFailure(err error) bool {
	var e execError
	return errors.As(err, &e)
}
