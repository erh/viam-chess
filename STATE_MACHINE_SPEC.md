# Chess Robot — State Machine Spec

Status: **implemented** (machine in `mode.go`, loop dispatch in `play.go`, command wiring in `do.go`; transition table covered by `mode_test.go`). Validate on-robot. Resolved decisions in §10.
Target branch: `state-machine-v2` (created from `upstream/main`, where `upstream = git@github.com:erh/viam-chess.git`)
Scope: the `chess` generic service (`module.go`, `do.go`, `play.go`, `state.go`, `config.go`). The `piece-finder` vision service is unchanged.

This spec describes **behavior**, not implementation. It is the single source of truth for the refactor; the prior chat exploring how to wire it into the code is intentionally dropped.

---

## 1. Purpose

The robot lives in a lobby and must be legible: at any moment it is in exactly one **mode** that fully determines its behavior. Today the only mode-like thing is a single `autoEnabled` boolean. This refactor replaces that with an explicit **6-mode state machine** with a validated transition table, generalizing the existing auto/loop behavior and adding vs-self, teaching (stub), error handling, and a rest/pause hub.

The module must still work after the refactor; unimplemented modes are placeholders, not broken paths.

---

## 2. Modes

| # | Name | Meaning |
|---|------|---------|
| 0 | **START** | No active game. Boot/entry hub, and where you land after a reset. Arm home. From here you choose an opponent. |
| 1 | **IDLE** | An active game, **suspended**. Universal pause reachable from any active state. Remembers its origin so it can resume. Also holds a *finished* game after game-over. |
| 2 | **VS_HUMAN** | Robot plays **black**, human plays **white** (hardcoded). The loop detects the human's ply and replies when it is black's turn. This is today's "auto" behavior. |
| 3 | **VS_SELF** | Robot plays **both** sides, one ply per loop tick. **Blind**: no per-move vision verification — it trusts the arm. |
| 4 | **TEACHING** | **Pure stub.** The transition works and game state is preserved, but there is no behavior yet. |
| 5 | **ERROR** | Entered automatically on an **execution failure**. Halts the loop and homes the arm. **Recoverable** — does not destroy game state. |

START and IDLE are mutually exclusive by a single rule: **a game either exists or it doesn't.** START = no game; IDLE = a game exists but is paused/finished.

---

## 3. State diagram

```
   boot ─▶ ┌─────────────────────┐
   ┌──────▶│  0  START (no game) │◀── reset ──┐
   │       │  entry hub          │            │
   │       └──┬───────────────┬──┘            │
   │     vs-human          vs-self            │
   │          ▼               ▼               │
   │   ┌────────────┐  ┌────────────┐         │
   │   │ 2 VS_HUMAN │  │ 3 VS_SELF  │         │
   │   └─┬───▲───┬──┘  └──┬──────┬──┘         │
   │ to4 │   │2  │pause   │pause │game-over   │
   │     ▼   │   │        │      │            │
   │ ┌───────┴┐  │        │      └────────────┤ (game-over)
   │ │4 TEACH │  │        │                    │
   │ └───┬────┘  │        │                    │
   │ pause│      │        │                    │
   │     ▼       ▼        ▼                    │
   │  ┌──────────────────────┐                 │
   └──┤ 1 IDLE (game paused) │── resume ─▶ origin (2/3/4)
      └──────────────────────┘
            ▲                  (game-over from 2/3 also lands here,
            │                   with resume disabled — reset only)
   ANY ─────┼───────────▶ 5 ERROR ──┬── resume(prevMode) if game intact
            └──── reset ◀───────────┴── reset ─▶ 0
```

---

## 4. Transition table (authoritative)

| From | Trigger | To | Notes |
|------|---------|-----|-------|
| (boot) | startup | START(0) | Always, regardless of any saved game. |
| START(0) | start vs-human | VS_HUMAN(2) | Assumes the physical board is already set up. |
| START(0) | start vs-self | VS_SELF(3) | Assumes the board is set up. |
| VS_HUMAN(2) | toggle teaching | TEACHING(4) | Game preserved. |
| TEACHING(4) | back to game | VS_HUMAN(2) | Game preserved. |
| {2,3,4} | pause | IDLE(1) | Records origin. |
| {2,3} | game-over (checkmate/stalemate/draw) | IDLE(1) | `gameOver` flag set → resume disabled. |
| IDLE(1) | resume | origin (2/3/4) | Only if paused (not `gameOver`). |
| IDLE(1) | reset | START(0) | Clears game state (see §6.4). |
| any | execution failure | ERROR(5) | Records `prevMode`. |
| ERROR(5) | resume | `prevMode` | Only if game state is intact. |
| ERROR(5) | reset | START(0) | Clears game state. |

**Illegal transitions** (any edge not in this table, e.g. START→TEACHING, or resume from a `gameOver` IDLE) are **rejected with an error and cause no mode change**.

---

## 5. Per-mode loop behavior

There is **one background loop**. Each tick, behavior is dispatched by the current mode:

| Mode | Capture + refresh live snapshot | Detect human move | Play a move | Move pieces with the arm |
|------|:---:|:---:|:---:|:---:|
| 0 START | yes | no | no | no |
| 1 IDLE | yes | no | no | no |
| 2 VS_HUMAN | yes | yes | reply on black's turn | yes (on reply) |
| 3 VS_SELF | yes | no | one ply per tick, alternating | yes |
| 4 TEACHING | yes | no | no | no |
| 5 ERROR | no (loop halted) | no | no | no (arm parked home) |

"Refresh live snapshot" keeps the companion app's board view current. In START/IDLE the robot never moves a **piece** (homing the arm to keep the camera clear is acceptable; the intent is "no gameplay movement").

---

## 6. Cross-cutting rules

### 6.1 Representation & triggering
- The mode is a single **atomic field** plus an **explicit transition table** that validates every edge. There is no scattered, ad-hoc transition logic.
- Mode is changed via `DoCommand {"mode": N}`.
- Setting the mode is **atomic and acknowledges instantly**. The physical effect lands at the **next tick / sub-step boundary**: an in-flight arm motion finishes first (no mid-move cancellation). A pause therefore feels instant to the caller even though the arm completes its current motion.

### 6.2 What counts as a failure (→ ERROR)
- **Only execution failures** trip ERROR: arm motion failure, gripper failing to grab after retries, engine crash, and similar hardware/engine faults.
- **Invalid input does NOT trip ERROR** — a malformed command, an illegal mode transition request, or a manually requested illegal move is **rejected with an error and leaves the mode unchanged**.
- **Transient vision noise does NOT trip ERROR** — it is absorbed by recapture/retry.
- **An illegal human board in VS_HUMAN does NOT trip ERROR** — see §7.4.

### 6.3 ERROR is recoverable
- On entering ERROR: record `prevMode`, halt the loop, home the arm, and **preserve `state.json`** (game data is never auto-deleted on error).
- Exit either by **resume → `prevMode`** (when game state is intact) or **reset → START(0)**.

### 6.4 Two distinct "resets"
- **State-machine reset (→ START(0))**: clears software game state and homes the arm **only**. It does **not** physically rearrange pieces.
- **Physical board reset** (arm rearranges pieces to the starting layout): a **separate explicit command**, never invoked automatically — and never from ERROR (the arm may be unsafe).

### 6.5 Restart
- The module **always boots into START(0)**, regardless of any saved game on disk.

### 6.6 Robot color
- VS_HUMAN: robot is **black**, human is **white** (hardcoded). The loop replies only when it is black's turn.

### 6.7 Difficulty
- Difficulty is **ELO-based** (the existing `difficulty` command). It may be changed in **any** mode and does not cause a transition.

---

## 7. Detailed semantics

### 7.1 START(0)
- No active game. Arm home. Loop captures for the live snapshot only.
- Exits: `start vs-human` → 2, `start vs-self` → 3.
- Starting a game **assumes the physical board is already correctly set up** and begins immediately (no verification, no auto-arrange).

### 7.2 IDLE(1)
- Reachable from any active state: **2, 3, and 4**.
- Tracks **`prevMode`** (origin) and a **`gameOver`** flag.
- **Paused** (gameOver = false): `resume` returns to `prevMode`; `reset` → START(0).
- **Finished** (gameOver = true, set when a game ends in 2/3): `resume` is **disabled**; only `reset` → START(0).
- Loop keeps the live snapshot fresh; the robot never moves a piece.

### 7.3 VS_SELF(3)
- Robot plays both sides, **one ply per loop tick**, alternating colors.
- **Blind**: it does not verify the board against expected state; it trusts the arm. (Only VS_HUMAN waits on an illegal board.)
- **Cadence is configurable** (think-time / tick interval).
- Plays **one** full game; on game-over → IDLE(1) (finished). **No auto-restart** — a human resets for the next game.

### 7.4 VS_HUMAN(2) — illegal / unsettled board
- When the detected board change cannot be mapped to a legal move:
  - **Stay in mode 2**, keep re-detecting, and **do not reply** until the board is legal.
  - **Surface a "needs-fix" flag** in the board snapshot so the app can prompt the human.
  - Transient noise self-heals via recapture; a persistently illegal board simply waits for a human to fix it.
- This is a **sub-state of mode 2**, not a separate mode. ERROR is reserved for hardware faults.

### 7.5 TEACHING(4)
- Stub. Transitions in/out work and **preserve the game**. Exits: back to VS_HUMAN(2), or pause → IDLE(1). No behavior to implement yet.

---

## 8. Known design seams (accepted, documented)

- **Game-over parks in IDLE, not START**, even though "0 = no game." A finished game sits in IDLE with `gameOver = true` (resume disabled) until reset. The flag disambiguates; IDLE is doing double duty as "paused" and "post-game." Accepted. (Clean alternative, if it ever grates: route game-over → START(0).)
- **IDLE and ERROR are both "from any state"** targets, but they are clearly different: IDLE is resumable; ERROR halts and needs resume-or-reset.

---

## 9. Existing capabilities to build on (factual, upstream/main)

The fresh session should **not** rebuild these — they already exist and the state machine wraps/gates them:

- **Background board loop** (`runBoardLoop` / `boardTick` in `play.go`), cadence via `board-loop-interval-ms`.
- **Auto / play-vs-human** via `autoEnabled` (bool) — robot plays black, detects human ply, replies. This is mode 2's engine.
- **Game-over / check / outcome** (`gameEventsResult` in `do.go`, `is_over`) — hook game-over transitions here.
- **Eval bar** (`lastScoreCP` / `lastScoreMate`).
- **Difficulty** via ELO (`applyElo`, `difficulty` command).
- **Vision-noise tolerance + mid-castle deferral** (`badDiffMaxAttempts` recapture loop, `isCastleSquarePair`) — the existing "debounce."
- **Snapshot cache** (`boardCache`, `board-snapshot`), **undo**, **PlayFEN**, **hover**, **promotion**, **en passant**, **castling**, **announce/ai-responder** (`on_move_target`).
- **Companion UI** lives in `viamapp/` (TypeScript). It reads `auto`, `difficulty`, and game-event fields from the snapshot.

---

## 10. Resolved implementation decisions

These were design choices left open in the original spec. Resolved during the implementation grilling on 2026-06-24; recorded here as the authoritative implementation contract.

### 10.1 Settled choices (from the original open questions)
- **`mode` replaces `autoEnabled`.** The `autoEnabled atomic.Bool` is deleted; the mode machine is the single source of truth. The snapshot keeps emitting `auto` (= `mode == VS_HUMAN`) **and** gains a new `mode` int, plus `game_over` and `needs_fix` bools. The `viamapp` UI is **not** modified — see the `auto` shim (§10.6).
- **START/IDLE/TEACHING loop behavior.** Keep ticking to refresh the snapshot (capture + home), but gate out detection / reply / piece-movement. "Never move" = never moves a *piece*; arm homing between ticks is allowed.
- **Debounce strength.** The existing recapture-on-noise + mid-castle deferral is sufficient. No "stable for K ticks" rule unless real misfires appear.

### 10.2 Representation (`mode.go`)
The machine is isolated in a new `mode.go`:
```go
type modeMachine struct {
    mu          sync.RWMutex
    mode        Mode // START..ERROR
    idleOrigin  Mode // where IDLE resumes to (set on →IDLE)
    errPrevMode Mode // where ERROR resumes to (set on →ERROR)
    gameOver    bool // set on game-over →IDLE; disables resume
}
```
`idleOrigin` and `errPrevMode` are **separate fields** (not one shared `prevMode`): a manual-command fault while *in IDLE* → ERROR → resume → IDLE must still know IDLE's own origin.

### 10.3 Triggering & concurrency
- `{"mode": N}` is **target-based**: it names a destination, and a single `transition(to)` method validates the `(current → N)` edge against the table plus guards (resume needs `!gameOver && to == origin`; `mode:0`/reset is always legal from IDLE/ERROR; **ERROR is never a command target** — `{"mode":5}` is rejected).
- **Mode-set is lock-free** (handled in `DoCommand` *before* `doCommandLock`, like the board-snapshot fast path). It only flips in-memory machine state and returns instantly, so a pause acks immediately while the in-flight arm motion finishes. All physical/persistent side-effects are deferred to the loop's next tick, dispatched by the new mode.
  - **One exception:** the `START → {VS_HUMAN, VS_SELF}` transition also wipes `state.json` inline. This is safe because START has no concurrent state writer, and it guarantees a fresh game (closing the boot-leftover race).

### 10.4 Error classification (→ ERROR)
- Arm / gripper / engine faults are wrapped in an `errExec` sentinel at their lowest call sites (`movement.go`, `pickMove`). A single `isExecFailure(err)` helper decides ERROR uniformly for **both** the loop and manual `cmd.Go`/`cmd.Move`.
- `checkPositionForMoves` (detection) errors are **plain** errors → `needs_fix`, never ERROR. Capture / vision-RPC failures are benign → log + skip tick.
- On entering ERROR: record `errPrevMode`, home the arm best-effort (may itself fail — logged), preserve `state.json`. The loop **halts by early-returning** each tick while `mode == ERROR`. Resume re-checks that `getGame` succeeds ("game intact").

### 10.5 Loop dispatch & tick ordering (`boardTick`)
`boardTick` dispatches on the mode read at tick start:
- **ERROR** → return immediately (halt).
- **START** → `ensureNoGame` (idempotent remove); home; capture; refresh.
- **IDLE / TEACHING** → home; capture; refresh. No detect / reply / move.
- **VS_HUMAN** → home; capture; refresh; detect (non-exec err → `needs_fix`, cleared on next clean detection); **game-over check before reply** (a human-delivered mate must not trigger a doomed `makeAMove`); reply on black's turn (`isExecFailure` → `enterError` + home); post-reply game-over check.
- **VS_SELF** → home; capture; refresh; `makeAMove(false)` blind (`isExecFailure` → `enterError` + home); post-move game-over check. Cadence reuses `board-loop-interval-ms` (tick) + `engine-millis` (think); turn alternation is implicit in `makeAMove`.
- Game-over in {2,3} → `enterIdle(gameOver=true)`.

### 10.6 `auto` compat shim & the two resets
- `{auto:true}`: START → start VS_HUMAN (fresh game); IDLE(origin==VS_HUMAN, `!gameOver`) → resume; already VS_HUMAN → no-op; else reject. `{auto:false}`: VS_HUMAN → pause to IDLE; else no-op. Both map onto `transition(...)`.
- **Two resets:** existing `{reset:true}` stays the **physical** board-rearrange (`resetBoard`), **rejected when `mode==ERROR`** (§6.4 — arm may be unsafe); this is the sole exception to "manual commands are ungated." New `{mode:0}` is the **software** state-reset (loop ensures no-game + homes, no rearrange).

### 10.7 Manual commands & testing
- `Move`, `Go`, `Undo`, `PlayFEN`, `Hover` remain ungated operator/debug tools (run under `doCommandLock` as today); only their exec-failures trip ERROR.
- A table-driven `mode_test.go` covers the transition machine (legal/illegal edges, origin tracking, gameOver guard, IDLE→ERROR→IDLE origin preservation). All other behavior is validated on-robot.

---

## 11. Git context

- Remote `upstream` = `git@github.com:erh/viam-chess.git` (the canonical repo).
- Work branch `state-machine-v2` is based on `upstream/main`.
- `origin` = `Rob1in/viam-chess` (a fork — was the source of earlier confusion; do not treat it as canonical).
