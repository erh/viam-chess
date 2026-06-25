package viamchess

import "testing"

// TestTransitionTable exercises every command-driven edge: each case configures
// the machine's state, attempts one transition, and checks acceptance plus the
// resulting mode.
func TestTransitionTable(t *testing.T) {
	cases := []struct {
		name       string
		mode       Mode
		idleOrigin Mode
		errPrev    Mode
		gameOver   bool
		to         Mode
		wantErr    bool
		wantMode   Mode // only checked when !wantErr
	}{
		// START: pick an opponent.
		{"start->vs_human", ModeStart, 0, 0, false, ModeVsHuman, false, ModeVsHuman},
		{"start->vs_self", ModeStart, 0, 0, false, ModeVsSelf, false, ModeVsSelf},
		{"start->teaching illegal", ModeStart, 0, 0, false, ModeTeaching, true, 0},
		{"start->idle illegal", ModeStart, 0, 0, false, ModeIdle, true, 0},
		{"start->start illegal", ModeStart, 0, 0, false, ModeStart, true, 0},
		{"start->error illegal", ModeStart, 0, 0, false, ModeError, true, 0},

		// VS_HUMAN: teaching toggle, pause. No direct reset, no jump to vs_self.
		{"vs_human->teaching", ModeVsHuman, 0, 0, false, ModeTeaching, false, ModeTeaching},
		{"vs_human->idle pause", ModeVsHuman, 0, 0, false, ModeIdle, false, ModeIdle},
		{"vs_human->start illegal", ModeVsHuman, 0, 0, false, ModeStart, true, 0},
		{"vs_human->vs_self illegal", ModeVsHuman, 0, 0, false, ModeVsSelf, true, 0},

		// VS_SELF: pause only.
		{"vs_self->idle pause", ModeVsSelf, 0, 0, false, ModeIdle, false, ModeIdle},
		{"vs_self->teaching illegal", ModeVsSelf, 0, 0, false, ModeTeaching, true, 0},
		{"vs_self->vs_human illegal", ModeVsSelf, 0, 0, false, ModeVsHuman, true, 0},

		// TEACHING: back to game, or pause.
		{"teaching->vs_human", ModeTeaching, 0, 0, false, ModeVsHuman, false, ModeVsHuman},
		{"teaching->idle pause", ModeTeaching, 0, 0, false, ModeIdle, false, ModeIdle},
		{"teaching->vs_self illegal", ModeTeaching, 0, 0, false, ModeVsSelf, true, 0},

		// IDLE paused: resume to origin or reset; other targets illegal.
		{"idle resume to origin", ModeIdle, ModeVsHuman, 0, false, ModeVsHuman, false, ModeVsHuman},
		{"idle resume self origin", ModeIdle, ModeVsSelf, 0, false, ModeVsSelf, false, ModeVsSelf},
		{"idle resume teaching origin", ModeIdle, ModeTeaching, 0, false, ModeTeaching, false, ModeTeaching},
		{"idle resume wrong target", ModeIdle, ModeVsHuman, 0, false, ModeVsSelf, true, 0},
		{"idle reset", ModeIdle, ModeVsHuman, 0, false, ModeStart, false, ModeStart},

		// IDLE finished (gameOver): resume disabled, reset only.
		{"idle gameOver resume disabled", ModeIdle, ModeVsHuman, 0, true, ModeVsHuman, true, 0},
		{"idle gameOver reset ok", ModeIdle, ModeVsHuman, 0, true, ModeStart, false, ModeStart},

		// ERROR: resume to errPrevMode or reset.
		{"error resume", ModeError, 0, ModeVsSelf, false, ModeVsSelf, false, ModeVsSelf},
		{"error resume wrong target", ModeError, 0, ModeVsSelf, false, ModeVsHuman, true, 0},
		{"error reset", ModeError, 0, ModeVsSelf, false, ModeStart, false, ModeStart},
		{"error->error illegal", ModeError, 0, ModeVsSelf, false, ModeError, true, 0},

		// Out-of-range target is never legal.
		{"bogus target", ModeVsHuman, 0, 0, false, Mode(9), true, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mm := &modeMachine{mode: tc.mode, idleOrigin: tc.idleOrigin, errPrevMode: tc.errPrev, gameOver: tc.gameOver}
			from, err := mm.transition(tc.to)
			if from != tc.mode {
				t.Fatalf("transition returned from=%v, want %v", from, tc.mode)
			}
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected illegal transition %v->%v, got nil err (mode now %v)", tc.mode, tc.to, mm.current())
				}
				if mm.current() != tc.mode {
					t.Fatalf("illegal transition changed mode: %v -> %v", tc.mode, mm.current())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error on %v->%v: %v", tc.mode, tc.to, err)
			}
			if mm.current() != tc.wantMode {
				t.Fatalf("after %v->%v: mode=%v, want %v", tc.mode, tc.to, mm.current(), tc.wantMode)
			}
		})
	}
}

// TestPauseResumeRecordsOrigin walks a full pause/resume cycle for each active mode.
func TestPauseResumeRecordsOrigin(t *testing.T) {
	for _, origin := range []Mode{ModeVsHuman, ModeVsSelf, ModeTeaching} {
		mm := &modeMachine{mode: origin}
		if _, err := mm.transition(ModeIdle); err != nil {
			t.Fatalf("pause from %v: %v", origin, err)
		}
		if got := mm.snapshot(); got.Mode != ModeIdle || got.IdleOrigin != origin || got.GameOver {
			t.Fatalf("after pause from %v: %+v", origin, got)
		}
		if _, err := mm.transition(origin); err != nil {
			t.Fatalf("resume to %v: %v", origin, err)
		}
		if mm.current() != origin {
			t.Fatalf("resume landed in %v, want %v", mm.current(), origin)
		}
	}
}

// TestGameOverDisablesResume checks the automatic game-over path parks in IDLE
// with resume disabled until reset.
func TestGameOverDisablesResume(t *testing.T) {
	mm := &modeMachine{mode: ModeVsHuman}
	mm.enterIdle(true)
	got := mm.snapshot()
	if got.Mode != ModeIdle || got.IdleOrigin != ModeVsHuman || !got.GameOver {
		t.Fatalf("after game-over: %+v", got)
	}
	if _, err := mm.transition(ModeVsHuman); err == nil {
		t.Fatal("resume should be disabled after game-over")
	}
	if _, err := mm.transition(ModeStart); err != nil {
		t.Fatalf("reset after game-over should work: %v", err)
	}
}

// TestErrorPreservesIdleOrigin is the key correctness case: a fault while paused
// (IDLE) must not clobber IDLE's own origin, so ERROR -> resume -> IDLE ->
// resume -> origin still works.
func TestErrorPreservesIdleOrigin(t *testing.T) {
	// Paused game whose origin is VS_HUMAN.
	mm := &modeMachine{mode: ModeVsHuman}
	if _, err := mm.transition(ModeIdle); err != nil {
		t.Fatalf("pause: %v", err)
	}

	// A manual command faults while paused.
	mm.enterError()
	got := mm.snapshot()
	if got.Mode != ModeError || got.ErrPrev != ModeIdle {
		t.Fatalf("after fault while paused: %+v", got)
	}
	if got.IdleOrigin != ModeVsHuman {
		t.Fatalf("ERROR clobbered idleOrigin: %+v", got)
	}

	// Resume the error back to IDLE.
	if _, err := mm.transition(ModeIdle); err != nil {
		t.Fatalf("error resume to idle: %v", err)
	}
	// IDLE still remembers its real origin.
	if _, err := mm.transition(ModeVsHuman); err != nil {
		t.Fatalf("idle resume to original origin: %v", err)
	}
	if mm.current() != ModeVsHuman {
		t.Fatalf("final mode %v, want VS_HUMAN", mm.current())
	}
}

// TestEnterErrorIdempotent confirms a second fault doesn't overwrite errPrevMode.
func TestEnterErrorIdempotent(t *testing.T) {
	mm := &modeMachine{mode: ModeVsSelf}
	mm.enterError()
	mm.enterError()
	if got := mm.snapshot(); got.ErrPrev != ModeVsSelf {
		t.Fatalf("errPrevMode overwritten by repeat fault: %+v", got)
	}
}
