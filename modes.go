package viamchess

import (
	"context"
	"errors"
	"fmt"

	"go.viam.com/rdk/services/statemachine"
)

// Mode is the legacy numeric view of what the robot is doing, kept for the
// {"mode": N} command, the mode field of every response and the companion
// app. The authoritative behavior now lives in the state machine service
// configured as `state-machine` (see examples/config1.json): each Mode is
// the name of one of its top-level states.
type Mode int

const (
	ModeStart    Mode = 0 // no active game; boot/entry hub
	ModeIdle     Mode = 1 // active game suspended (paused) or finished (game over)
	ModeVsHuman  Mode = 2 // robot plays black, human plays white
	ModeVsSelf   Mode = 3 // robot plays both sides
	ModeTeaching Mode = 4 // game preserved, robot passive
	ModeError    Mode = 5 // execution failure; robot homed, waiting for a human
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

// State machine state names and event names the chess service and its
// tasks agree on with the machine definition.
const (
	StateStart    = "start"
	StateIdle     = "idle"
	StateGameOver = "game_over"
	StateVsHuman  = "vs_human"
	StateVsSelf   = "vs_self"
	StateTeaching = "teaching"
	StateError    = "error"

	EventModeStart    = "mode.start"
	EventModeVsHuman  = "mode.vs_human"
	EventModeVsSelf   = "mode.vs_self"
	EventModeTeaching = "mode.teaching"
	EventPause        = "pause"
	EventResume       = "resume"
	EventError        = "error"
)

// modeFromState maps the machine's active state onto the legacy mode plus the
// game-over flag. A faulted machine reads as ERROR whatever its path.
func modeFromState(st statemachine.State) (mode Mode, gameOver bool) {
	if st.Status == statemachine.StatusFaulted {
		return ModeError, false
	}
	if len(st.Path) == 0 {
		return ModeStart, false
	}
	switch st.Path[0] {
	case StateIdle:
		return ModeIdle, false
	case StateGameOver:
		return ModeIdle, true
	case StateVsHuman:
		return ModeVsHuman, false
	case StateVsSelf:
		return ModeVsSelf, false
	case StateTeaching:
		return ModeTeaching, false
	case StateError:
		return ModeError, false
	default:
		return ModeStart, false
	}
}

// eventForMode is the event the legacy {"mode": N} command sends. Whether the
// machine accepts it from its current state is the machine definition's call.
func eventForMode(to Mode) (string, error) {
	switch to {
	case ModeStart:
		return EventModeStart, nil
	case ModeIdle:
		return EventPause, nil
	case ModeVsHuman:
		return EventModeVsHuman, nil
	case ModeVsSelf:
		return EventModeVsSelf, nil
	case ModeTeaching:
		return EventModeTeaching, nil
	default:
		return "", fmt.Errorf("mode %d cannot be requested", int(to))
	}
}

var errNoStateMachine = errors.New("no state-machine configured; modes are driven by the state machine service")

// machineState reads the state machine, if one is configured.
func (s *viamChessChess) machineState(ctx context.Context) (statemachine.State, error) {
	if s.stateMachine == nil {
		return statemachine.State{}, errNoStateMachine
	}
	return s.stateMachine.GetState(ctx, nil)
}

// currentMode is the legacy mode derived from the machine. Without a machine
// the robot is permanently START: manual commands only.
func (s *viamChessChess) currentMode(ctx context.Context) Mode {
	mode, _, _ := s.modeFields(ctx)
	return mode
}

// modeFields returns the mode-derived snapshot fields. auto = (mode==VS_HUMAN)
// is kept for backward compatibility alongside the mode int.
func (s *viamChessChess) modeFields(ctx context.Context) (mode Mode, auto, gameOver bool) {
	st, err := s.machineState(ctx)
	if err != nil {
		if !errors.Is(err, errNoStateMachine) {
			s.logger.Warnf("cannot read state machine: %v", err)
		}
		return ModeStart, false, false
	}
	mode, gameOver = modeFromState(st)
	return mode, mode == ModeVsHuman, gameOver
}

// withMode adds the current mode (0-5) to every DoCommand success payload.
func (s *viamChessChess) withMode(ctx context.Context, result map[string]interface{}) map[string]interface{} {
	mode := int(s.currentMode(ctx))
	if result == nil {
		return map[string]interface{}{"mode": mode}
	}
	result["mode"] = mode
	return result
}

// sendEvent delivers an event to the state machine and reports whether it
// took a transition.
func (s *viamChessChess) sendEvent(ctx context.Context, event string, payload map[string]interface{}) (bool, error) {
	if s.stateMachine == nil {
		return false, errNoStateMachine
	}
	res, err := s.stateMachine.SendEvent(ctx, event, payload, nil)
	if err != nil {
		return false, err
	}
	s.logger.Infof("event %s -> handled=%v path=%v", event, res.Handled, res.Path)
	return res.Handled, nil
}

// setMode applies the {"mode":N} command by sending the matching event. The
// machine definition decides which transitions are legal; an event it does
// not handle in its current state is an illegal transition.
func (s *viamChessChess) setMode(ctx context.Context, to Mode) (map[string]interface{}, error) {
	event, err := eventForMode(to)
	if err != nil {
		return nil, err
	}
	from := s.currentMode(ctx)
	handled, err := s.sendEvent(ctx, event, nil)
	if err != nil {
		return nil, err
	}
	if !handled {
		return nil, fmt.Errorf("illegal transition %v -> %v (event %q not handled)", from, to, event)
	}
	return s.withMode(ctx, nil), nil
}

// applyAutoShim maps the legacy {"auto":bool} command (still sent by the
// companion UI) onto events, so the unmodified UI keeps working.
func (s *viamChessChess) applyAutoShim(ctx context.Context, on bool) (map[string]interface{}, error) {
	cur := s.currentMode(ctx)
	if on {
		switch cur {
		case ModeVsHuman:
			// already playing as black — no-op
		case ModeStart, ModeIdle:
			if _, err := s.setMode(ctx, ModeVsHuman); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("auto:true not valid from %v", cur)
		}
	} else if cur == ModeVsHuman {
		if _, err := s.setMode(ctx, ModeIdle); err != nil {
			return nil, err
		}
	}
	// auto:false from a non-playing mode is a no-op.
	return s.withMode(ctx, map[string]interface{}{"auto": s.currentMode(ctx) == ModeVsHuman}), nil
}

// manualErr reports an execution fault from a manual command to the state
// machine as an "error" event, so the machine enters its error state and
// homes the arm; other errors pass through unchanged.
func (s *viamChessChess) manualErr(ctx context.Context, err error) error {
	if err != nil && isExecFailure(err) {
		s.logger.Errorf("manual command execution failure: %v", err)
		if s.stateMachine != nil {
			if _, sendErr := s.sendEvent(ctx, EventError, map[string]interface{}{"error": err.Error()}); sendErr != nil {
				s.logger.Warnf("cannot report execution failure to the state machine: %v", sendErr)
			}
		}
	}
	return err
}

// toModeInt coerces a DoCommand "mode" value to an int. Numbers arrive as
// float64 over gRPC.
func toModeInt(raw interface{}) (int, error) {
	switch v := raw.(type) {
	case float64:
		return int(v), nil
	case float32:
		return int(v), nil
	case int:
		return v, nil
	case int64:
		return int(v), nil
	default:
		return 0, fmt.Errorf("mode must be a number, got %T", raw)
	}
}

// execError marks a failure as an execution fault (arm/gripper/engine) — the
// only kind that reaches the state machine as an error. Detection, vision,
// and invalid-input errors stay unwrapped and are handled benignly.
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
// fault, so tasks and manual command paths can decide uniformly.
func isExecFailure(err error) bool {
	var e execError
	return errors.As(err, &e)
}
