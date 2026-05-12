# Chess

## To get your own
* Get all the hardware
    * https://docs.google.com/document/d/1T_ZlkjAxhhUZB5_EhZ64C6UP2SrXKVqBU8UHFkGirrw/edit?usp=sharing
* Make a table that looks like
    * https://photos.app.goo.gl/GDuryUgFtMPufmRD7
* Create a Viam machine
* Use the sample config in examples/config1.txt
* Ask questions when it invariably doesn't work

## chess config

### Required
| field | description |
| --- | --- |
| `piece-finder` | name of the piece-finder vision service |
| `arm` | name of the arm component |
| `gripper` | name of the gripper component |
| `pose-start` | name of the pose-start service (the home pose used between moves) |

### Optional
| field | type | default | description |
| --- | --- | --- | --- |
| `camera` | string | — | camera resource name; required only when `capture-dir` is set |
| `video-saver` | string | — | optional video-saver resource for clip recording per move |
| `engine` | string | `stockfish` | UCI engine binary to invoke for opponent moves |
| `engine-millis` | int | `10` | per-move thinking time for the engine, in milliseconds |
| `skill-adjust` | float | `50.0` | initial skill knob (multiplier on `engine-millis`); 50 = 1×, <50 weaker, >50 stronger |
| `capture-dir` | string | — | directory to dump pointcloud/image captures (mostly for VLA data); needs `camera` |
| `grab-z` | float | `40.0` | gripper Z height (mm) when picking up short pieces (pawn, rook, bishop, knight) |
| `grab-z-tall` | float | `80.0` | gripper Z height (mm) when picking up tall pieces (king, queen) |
| `graveyard-z` | float | `60.0` | gripper Z height (mm) when picking up / placing in graveyard slots |
| `graveyard-spacing-y` | float | `80.0` | spacing (mm) between graveyard rows; row 1 is one step off the board's a-file (white) or h-file (black), row 2 is two steps |
| `gripper-open-pos` | float | `450.0` | gripper open position (servo units) |
| `bad-diff-max-attempts` | int | `10` | retries when human-move detection sees a noisy "bad number of differences" |

### Example
```json
{
    "piece-finder": "piece-finder",
    "arm": "arm",
    "gripper": "gripper",
    "pose-start": "<pose>"
}
```

## Pawn promotion setup

Pawn promotion is auto-queen and uses the **first slot of each color's graveyard** to hold a spare queen. Before each game, place:

* a **white queen** at white's graveyard slot 0 (the row-1 position closest to a8)
* a **black queen** at black's graveyard slot 0 (the row-1 position closest to h1)

The slot's physical XY is computed from `graveyard-spacing-y` and the board's a-file / h-file edges; slot 0 is one `graveyard-spacing-y` step off the board, and pieces are picked up at `graveyard-z`. Captured pieces fill slots 1, 2, …, so slot 0 is never overwritten by normal play.

When a pawn promotes, the robot:

1. (if a capture) evicts the captured piece from the promotion square to the opposing graveyard,
2. moves the promoted pawn directly from rank 7 / 2 into the next free graveyard slot of its own color,
3. picks up the spare queen from slot 0 and places it on the promotion square.

Reset restores the spare queen back into slot 0 automatically.

Only one promotion per color per game is supported (slot 0 holds a single spare). Undoing through a promotion move is not supported.

## DoCommand reference

All commands are sent via `DoCommand` on the chess generic service. Pass exactly one of the keys below per call. Squares are lowercase algebraic (`a1`–`h8`).

| key | payload | description |
| --- | --- | --- |
| `move` | `{"from": "<sq>", "to": "<sq>", "n": <int>}` | Physically pick up the piece at `from` and place it at `to`. Repeats `n` times, alternating direction each iteration (so `n=2` ends back where it started). Captures are recorded in the graveyard. |
| `go` | `<int>` | Let the engine play this many moves and execute them physically. Returns the last move's UCI string. |
| `reset` | `true` | Return every piece to its initial-game home square, including pulling captured pieces back from the graveyard and restoring the spare queen to slot 0 if a promotion happened. |
| `wipe` | `true` | Clear saved game state and the cached square positions. |
| `clear-cache` | `true` | Clear only the square-position cache (forces re-scan from the next pointcloud capture). Use after physically nudging the board. |
| `skill` | `<float>` | Adjust engine strength. 50 = baseline; <50 weaker (linear scale-down of think time), >50 stronger (think time grows by `(skill-50)*2`). |
| `hover` | `"<sq>"` | Move the gripper to ~100 mm above the given square's pickup point and stay there. Does not return home. |
| `undo` | `<int>` | Physically undo the last N moves (newest-first), restoring captured pieces from the graveyard. Errors if any of the undone moves is a promotion. |
| `play-fen` | `"<path>"` | Wipe state, then replay every move from a PGN file at the given path. |
| `board-snapshot` | `true` | Return `{"fen", "camera_board", "white_graveyard", "black_graveyard"}` — the engine's FEN, what the camera currently sees per square, and the FEN-letter graveyard contents. |
| `detect-human-move` | `true` | Snapshot the camera and try to infer the human's most recent move. Returns `{"detected", "from", "to", "uci", "captured"}`. |

### Examples

```json
{"go": 1}
{"move": {"from": "e2", "to": "e4", "n": 1}}
{"hover": "e4"}
{"reset": true}
{"undo": 2}
{"play-fen": "data/sample.pgn"}
{"board-snapshot": true}
```

## piece finder config
```json
{
    "input": "<cropped-camera>"
}
```
