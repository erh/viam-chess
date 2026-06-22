# viam-chess

**A robot that plays physical chess against you.** A camera watches the board, a
vision model reads the position, the Stockfish engine picks a reply, and a robot
arm reaches in and moves the piece — then waits for your turn.

---

## Overview

`viam-chess` is a [Viam](https://www.viam.com) **module**. A module is a plugin
that adds new capabilities ("models") to a robot running
[`viam-server`](https://docs.viam.com/set-up-a-machine/viam-agent-and-server/). Install it from the
[Viam registry](https://docs.viam.com/registry/) and the models below become
available to add to any machine.

This module ships two models:

| Model | API | What it is |
| --- | --- | --- |
| `erh:viam-chess:chess` | generic service | The brains. Tracks the game, talks to the chess engine, and drives the arm. You send it commands; it plays chess. |
| `erh:viam-chess:piece-finder` | vision service | The eyes. Turns a camera frame into "what piece is on each square." The `chess` service depends on it. |

> **New to Viam?** A few terms used below:
> - **Component** — a piece of hardware (an `arm`, a `gripper`, a `camera`).
> - **Service** — higher-level software (vision, motion planning, or — here — chess).
> - **Resource** — any component or service, referenced by the name you give it
>   in config.
> - **`DoCommand`** — a generic request/response call every resource supports;
>   it's how you drive the `chess` service (see [Command reference](#command-reference)).
>
> You don't need deep Viam knowledge to run this — the
> [Getting started](#getting-started) section walks through it — but the
> [docs](https://docs.viam.com) explain each concept in depth.

## How it works

Each turn flows through three phases — perception, decision, actuation — and then
loops back for your move:

```
   your move
      │
      ▼
   PERCEPTION
      ├─ Capture depth & color data ......... camera component
      ├─ Identify pieces & board state ...... vision service
      └─ Detect the human's move ............ vision service
      │
      ▼
   DECISION
      ├─ Track pieces & update history
      └─ Calculate the next move ............ Stockfish engine
      │
      ▼
   ACTUATION
      ├─ Convert move into 3D pose .......... frame system
      ├─ Plan a safe arm trajectory ......... motion service
      └─ Move arm & gripper ................. motion service
      │
      └──▶ back to your move (next turn)
```

Concretely, the `chess` service depends on these resources, which you wire up in
config: a `piece-finder` vision service, an `arm`, a `gripper`, a `pose-start`
switch (a fixed "home" pose the arm returns to between moves), the built-in
`motion` service, and optionally a `camera`. The chess engine is an external
[UCI](https://en.wikipedia.org/wiki/Universal_Chess_Interface) binary —
[Stockfish](https://stockfishchess.org/) by default.

## Getting started

1. **Hardware.** You need a robot arm, a gripper, a depth+color camera mounted
   over the board, and a chessboard. See the
   [hardware build guide](https://docs.google.com/document/d/1T_ZlkjAxhhUZB5_EhZ64C6UP2SrXKVqBU8UHFkGirrw/edit?usp=sharing)
   for the exact parts and the
   [table layout](https://photos.app.goo.gl/GDuryUgFtMPufmRD7).
2. **A Viam machine.** [Create one](https://docs.viam.com/set-up-a-machine/first-machine/) and
   get `viam-server` running on the computer attached to your arm.
3. **A chess engine.** Install Stockfish on that machine
   (`apt-get install stockfish` puts it at `/usr/games/stockfish`).
4. **Add the module.** Add `erh:viam-chess` from the registry, then configure the
   `piece-finder` and `chess` services. The fastest way to see a working
   layout — arm, camera, vision service, and chess service wired together — is
   [`examples/config1.json`](examples/config1.json).
5. **Play.** Make your move on the physical board, then tell the robot to reply
   (see [Usage](#usage)).

## Usage

There are three ways to drive the robot once it's configured:

- **Web app.** This module ships a single-machine [Viam application](https://chess-control_viam-labs.viamapplications.com/),
  `chess-control`, for playing from the browser — board
  view, move buttons, eval bar, and difficulty control. It appears in the Viam
  app once the module is installed. Source: [`viamapp/`](viamapp/).
- **`DoCommand`.** Send commands directly to the `chess` service from any Viam
  SDK or the app's CONTROL tab. This is the underlying API the other methods use — see
  the [Command reference](#command-reference).
- **CLI.** [`cmd/cli`](cmd/cli/main.go) builds a `chesscli` binary (`make cli`)
  that wraps the same commands for quick terminal testing, e.g.
  `./chesscli -host <machine> -cmd go -n 1`. This is recommended for development.

A typical loop: you move a piece, then send `{"go": 1}` and the robot detects
your move, replies, and moves its piece.

## Configuration reference

### chess config

Configure the `erh:viam-chess:chess` service. Resource fields take the **name**
of the resource as configured elsewhere on the machine.

#### Required

| field | description |
| --- | --- |
| `piece-finder` | name of the `piece-finder` vision service |
| `arm` | name of the arm component |
| `gripper` | name of the gripper component |
| `pose-start` | name of the pose-start switch (the home pose used between moves) |

#### Optional

| field | type | default | description |
| --- | --- | --- | --- |
| `camera` | string | — | camera resource name; required only when `capture-dir` is set |
| `video-saver` | string | — | optional video-saver resource for clip recording per move |
| `engine` | string | `stockfish` | UCI engine binary to invoke for opponent moves |
| `engine-millis` | int | `10` | per-move thinking time for the engine, in milliseconds |
| `elo` | int | `1500` | engine strength as a target Elo rating; see [Engine strength](#engine-strength) |
| `capture-dir` | string | — | directory to dump pointcloud/image captures (mostly for VLA data); needs `camera` |
| `grab-z` | float | `40.0` | gripper Z height (mm) when picking up short pieces (pawn, rook, bishop, knight) |
| `grab-z-tall` | float | `80.0` | gripper Z height (mm) when picking up tall pieces (king, queen) |
| `graveyard-z` | float | `60.0` | gripper Z height (mm) when picking up / placing in graveyard slots |
| `graveyard-spacing-y` | float | `80.0` | spacing (mm) between graveyard rows; row 1 is one step off the board's a-file (white) or h-file (black), row 2 is two steps |
| `gripper-open-pos` | float | `450.0` | gripper open position (servo units) |
| `bad-diff-max-attempts` | int | `10` | retries when human-move detection sees a noisy "bad number of differences" |
| `board-loop-interval-ms` | int | `0` (disabled) | cadence (ms) of the background board-scan loop; when disabled, `board-snapshot` falls back to a per-call camera capture |
| `companion-bad-state-delay-ms` | int | `60000` | web-app companion: delay before showing a "board looks wrong" hint |
| `companion-welcome-revive-ms` | int | `60000` | web-app companion: idle time before the welcome message reappears |
| `companion-in-check-dismiss-ms` | int | `8000` | web-app companion: how long the "check" message stays up |
| `companion-first-move-dismiss-ms` | int | `8000` | web-app companion: how long the first-move hint stays up |
| `on_move_target` | string | — | name of a generic service that receives a `move_made` event after every engine move (optional, fire-and-forget); toggle at runtime with the `set-announce` command |

#### Example

```json
{
    "piece-finder": "piece-finder",
    "arm": "arm",
    "gripper": "gripper",
    "pose-start": "<pose>"
}
```

### piece finder config

Configure the `erh:viam-chess:piece-finder` vision service. It reads a camera
frame and reports the piece (or emptiness) on each square; the `chess` service
consumes this. Point `input` at a camera that is cropped to just the board.

```json
{
    "input": "<cropped-camera>"
}
```

### Engine strength

The engine plays at a target Elo rating, set with the `elo` config field
(default `1500`) or changed at runtime with the `difficulty` command (see the
[Command reference](#command-reference)). The value is applied via the UCI
`UCI_LimitStrength` / `UCI_Elo` options and is clamped to the range the engine
reports — out-of-range values are clamped with a warning, and the actually
applied value is returned.

### Pawn promotion setup

Pawn promotion is auto-queen and uses the **first slot of each color's
graveyard** to hold a spare queen. Before each game, place:

* a **white queen** at white's graveyard slot 0 (the row-1 position closest to a8)
* a **black queen** at black's graveyard slot 0 (the row-1 position closest to h1)

The slot's physical XY is computed from `graveyard-spacing-y` and the board's
a-file / h-file edges; slot 0 is one `graveyard-spacing-y` step off the board,
and pieces are picked up at `graveyard-z`. Captured pieces fill slots 1, 2, …, so
slot 0 is never overwritten by normal play.

When a pawn promotes, the robot:

1. (if a capture) evicts the captured piece from the promotion square to the opposing graveyard,
2. moves the promoted pawn directly from rank 7 / 2 into the next free graveyard slot of its own color,
3. picks up the spare queen from slot 0 and places it on the promotion square.

Reset restores the spare queen back into slot 0 automatically.

Only one promotion per color per game is supported (slot 0 holds a single
spare). Undoing through a promotion move is not supported.

## Command reference

All commands are sent via `DoCommand` on the `chess` generic service. Pass
exactly one of the keys below per call. Squares are lowercase algebraic
(`a1`–`h8`).

| key | payload | description |
| --- | --- | --- |
| `move` | `{"from": "<sq>", "to": "<sq>", "n": <int>}` | Physically pick up the piece at `from` and place it at `to`. Repeats `n` times, alternating direction each iteration (so `n=2` ends back where it started). Captures are recorded in the graveyard. |
| `go` | `<int>` | Let the engine play this many moves and execute them physically. Returns the last move's UCI string. |
| `reset` | `true` | Return every piece to its initial-game home square, including pulling captured pieces back from the graveyard and restoring the spare queen to slot 0 if a promotion happened. |
| `undo` | `<int>` | Physically undo the last N moves (newest-first), restoring captured pieces from the graveyard. Errors if any of the undone moves is a promotion. |
| `wipe` | `true` | Clear saved game state and the cached square positions. |
| `clear-cache` | `true` | Clear only the square-position cache (forces re-scan from the next pointcloud capture). Use after physically nudging the board. |
| `difficulty` | `<int>` | Change engine strength at runtime to a target Elo. See [Engine strength](#engine-strength). Returns `{"difficulty": <applied-elo>}`. |
| `hover` | `"<sq>"` | Move the gripper to ~100 mm above the given square's pickup point and stay there. Does not return home. |
| `auto` | `true` / `false` | Enable/disable the engine's automatic reply in the background board loop (detection and cache refresh still run when off). Returns `{"auto": <bool>}`. |
| `set-announce` | `true` / `false` | Enable/disable dispatching `move_made` events to `on_move_target`. Returns `{"announce": <bool>}`. |
| `play-fen` | `"<path>"` | Wipe state, then replay every move from a PGN file at the given path. |
| `board-snapshot` | `true` | Return the current board state — see [Snapshot fields](#snapshot-fields). Served from a cache when the board loop is running. |
| `game-events` | `true` | Return just the game-state fields (no camera read): `event`, `outcome`, `method`, `turn`, `in_check`, `is_over`, `score_cp`, `score_mate`. |
| `companion-config` | `true` | Return the web-app companion timing values: `bad_state_delay_ms`, `welcome_revive_ms`, `in_check_dismiss_ms`, `first_move_dismiss_ms`. |

#### Snapshot fields

`board-snapshot` returns:

| field | description |
| --- | --- |
| `fen` | the engine's current position in FEN |
| `camera_board` | what the camera currently sees per square |
| `white_graveyard` / `black_graveyard` | FEN-letter contents of each graveyard |
| `auto` | whether the engine auto-reply is enabled |
| `captured_at_ms` | capture time (Unix ms) |
| `event` | highest-priority active event: `checkmate`, `stalemate`, `draw`, `check`, or `none` |
| `outcome` | `in_progress`, `white_won`, `black_won`, or `draw` |
| `method` | how the game ended, or `none` while in progress |
| `turn` | `white` or `black` |
| `in_check` | true if the side to move is in check |
| `is_over` | true if the game has ended |
| `score_cp` | engine evaluation in centipawns, white-relative (positive = white ahead) |
| `score_mate` | moves to forced mate, white-relative (positive = white mates, negative = black mates, 0 = none) |

### Examples

```json
{"go": 1}
{"move": {"from": "e2", "to": "e4", "n": 1}}
{"hover": "e4"}
{"reset": true}
{"undo": 2}
{"difficulty": 1800}
{"auto": true}
{"play-fen": "data/sample.pgn"}
{"board-snapshot": true}
```

## Support

This is a community module maintained at
[github.com/erh/viam-chess](https://github.com/erh/viam-chess). Open an issue
with questions — and, as the build guide says, ask when it invariably doesn't
work the first time.
