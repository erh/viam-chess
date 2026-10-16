package viamchess

import (
	"encoding/json"
	"os"
	"testing"

	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/services/statemachine"
	smbuiltin "go.viam.com/rdk/services/statemachine/builtin"
	"go.viam.com/rdk/services/task"
	"go.viam.com/rdk/utils"
)

func TestModeFromState(t *testing.T) {
	cases := []struct {
		path     []string
		status   statemachine.Status
		mode     Mode
		gameOver bool
	}{
		{[]string{"start", "watching"}, statemachine.StatusRunning, ModeStart, false},
		{[]string{"idle"}, statemachine.StatusRunning, ModeIdle, false},
		{[]string{"game_over"}, statemachine.StatusRunning, ModeIdle, true},
		{[]string{"vs_human", "replying"}, statemachine.StatusRunning, ModeVsHuman, false},
		{[]string{"vs_self", "playing"}, statemachine.StatusRunning, ModeVsSelf, false},
		{[]string{"teaching"}, statemachine.StatusRunning, ModeTeaching, false},
		{[]string{"error"}, statemachine.StatusRunning, ModeError, false},
		{[]string{"vs_human", "waiting"}, statemachine.StatusFaulted, ModeError, false},
		{nil, statemachine.StatusRunning, ModeStart, false},
		{[]string{"something_else"}, statemachine.StatusRunning, ModeStart, false},
	}
	for _, tc := range cases {
		mode, gameOver := modeFromState(statemachine.State{Path: tc.path, Status: tc.status})
		if mode != tc.mode || gameOver != tc.gameOver {
			t.Errorf("modeFromState(%v, %v) = %v, %v; want %v, %v", tc.path, tc.status, mode, gameOver, tc.mode, tc.gameOver)
		}
	}
}

func TestEventForMode(t *testing.T) {
	want := map[Mode]string{
		ModeStart:    EventModeStart,
		ModeIdle:     EventPause,
		ModeVsHuman:  EventModeVsHuman,
		ModeVsSelf:   EventModeVsSelf,
		ModeTeaching: EventModeTeaching,
	}
	for mode, event := range want {
		got, err := eventForMode(mode)
		if err != nil || got != event {
			t.Errorf("eventForMode(%v) = %q, %v; want %q", mode, got, err, event)
		}
	}
	if _, err := eventForMode(ModeError); err == nil {
		t.Error("ERROR must not be requestable")
	}
	if _, err := eventForMode(Mode(9)); err == nil {
		t.Error("bogus mode must not be requestable")
	}
}

// exampleMachine loads the chess-modes state machine from examples/config1.json
// as the rdk builtin would see it.
func exampleMachine(t *testing.T) (*smbuiltin.Config, map[string]interface{}) {
	t.Helper()
	data, err := os.ReadFile("examples/config1.json")
	if err != nil {
		t.Fatal(err)
	}
	var conf struct {
		Services []struct {
			Name       string                 `json:"name"`
			API        string                 `json:"api"`
			Attributes map[string]interface{} `json:"attributes"`
		} `json:"services"`
	}
	if err := json.Unmarshal(data, &conf); err != nil {
		t.Fatal(err)
	}
	for _, s := range conf.Services {
		if s.API != statemachine.API.String() {
			continue
		}
		cfg, err := resource.TransformAttributeMap[*smbuiltin.Config](utils.AttributeMap(s.Attributes))
		if err != nil {
			t.Fatal(err)
		}
		return cfg, s.Attributes
	}
	t.Fatal("no state machine in examples/config1.json")
	return nil, nil
}

// TestExampleMachineDefinition checks the shipped machine: it validates, its
// states only run this module's tasks, the chess service, and every top-level
// state is one the mode mapping understands.
func TestExampleMachineDefinition(t *testing.T) {
	cfg, _ := exampleMachine(t)
	deps, _, err := cfg.Validate("chess-modes")
	if err != nil {
		t.Fatalf("example state machine does not validate: %v", err)
	}
	wantDeps := map[string]bool{
		task.Named("watch-board").String():     true,
		task.Named("wait-human-move").String(): true,
		task.Named("engine-move").String():     true,
		task.Named("home").String():            true,
		"chess":                                true,
	}
	for _, d := range deps {
		if !wantDeps[d] {
			t.Errorf("unexpected dependency %q", d)
		}
		delete(wantDeps, d)
	}
	for d := range wantDeps {
		t.Errorf("missing dependency %q", d)
	}

	known := map[string]bool{
		StateStart: true, StateIdle: true, StateGameOver: true, StateVsHuman: true,
		StateVsSelf: true, StateTeaching: true, StateError: true, "stuck": true,
	}
	for name := range cfg.States {
		if !known[name] {
			t.Errorf("top-level state %q has no legacy mode mapping", name)
		}
	}
	if cfg.Initial != StateStart {
		t.Errorf("initial state is %q, want %q", cfg.Initial, StateStart)
	}

	// Every mode the legacy command can request has at least one state that handles it.
	for _, mode := range []Mode{ModeStart, ModeIdle, ModeVsHuman, ModeVsSelf, ModeTeaching} {
		event, _ := eventForMode(mode)
		handled := false
		for _, st := range cfg.States {
			if _, ok := st.On[event]; ok {
				handled = true
			}
		}
		if !handled {
			t.Errorf("event %q for mode %v is handled by no state", event, mode)
		}
	}
	// Errors anywhere bubble to the error state, which parks the arm.
	if cfg.On[EventError] != StateError {
		t.Errorf("machine-level error handler is %q, want %q", cfg.On[EventError], StateError)
	}
	if cfg.States[StateError].Task == nil || cfg.States[StateError].Task.Name != "home" {
		t.Error("error state must run the home task")
	}
}
