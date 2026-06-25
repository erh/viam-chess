package viamchess

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/corentings/chess/v2"
	"github.com/mitchellh/mapstructure"

	"go.viam.com/rdk/vision/viscapture"
	"go.viam.com/utils/trace"
)

type MoveCmd struct {
	From, To string
	N        int
}

type cmdStruct struct {
	Move            MoveCmd
	Go              int
	Reset           bool
	Wipe            bool
	Difficulty      int
	Hover           string
	ClearCache      bool `mapstructure:"clear-cache"`
	Undo            int
	PlayFEN         string `mapstructure:"play-fen"`
	BoardSnapshot   bool   `mapstructure:"board-snapshot"`
	GameEvents      bool   `mapstructure:"game-events"`
	CompanionConfig bool   `mapstructure:"companion-config"`
	SetAnnounce     *bool  `mapstructure:"set-announce"` // pointer so explicit false is distinguishable from absent
}

func (s *viamChessChess) DoCommand(ctx context.Context, cmdMap map[string]interface{}) (map[string]interface{}, error) {
	s.doCommandCount.Add(1)
	ctx, span := trace.StartSpan(ctx, "chess::DoCommand")
	defer span.End()

	// board-snapshot fast path: serve from cache without blocking on doCommandLock.
	// The board loop holds doCommandLock during makeAMove, so without this early
	// return polling clients would see stale state for the entire arm movement.
	if bs, _ := cmdMap["board-snapshot"].(bool); bs {
		mode, auto, gameOver := s.modeFields()
		s.boardCache.mu.RLock()
		if s.boardCache.ready {
			result := map[string]interface{}{
				"fen":             s.boardCache.fen,
				"camera_board":    s.boardCache.cameraBoard,
				"white_graveyard": s.boardCache.whiteGraveyard,
				"black_graveyard": s.boardCache.blackGraveyard,
				"auto":            auto,
				"mode":            mode,
				"game_over":       gameOver,
				"needs_fix":       s.boardCache.needsFix,
				"captured_at_ms":  s.boardCache.capturedAt.UnixMilli(),
				"event":           s.boardCache.gameEvents.Event,
				"outcome":         s.boardCache.gameEvents.Outcome,
				"method":          s.boardCache.gameEvents.Method,
				"turn":            s.boardCache.gameEvents.Turn,
				"in_check":        s.boardCache.gameEvents.InCheck,
				"is_over":         s.boardCache.gameEvents.IsOver,
				"score_cp":        s.boardCache.gameEvents.ScoreCP,
				"score_mate":      s.boardCache.gameEvents.ScoreMate,
			}
			s.boardCache.mu.RUnlock()
			return result, nil
		}
		s.boardCache.mu.RUnlock()
	}

	// mode / auto fast path: changing the mode is lock-free (§10.3), so a pause
	// acks instantly even while the board loop holds doCommandLock for an
	// in-flight arm move. Handled before Decode (numbers arrive as float64).
	if raw, ok := cmdMap["mode"]; ok {
		to, err := toModeInt(raw)
		if err != nil {
			return nil, err
		}
		return s.setMode(ctx, Mode(to))
	}
	if raw, ok := cmdMap["auto"]; ok {
		on, ok := raw.(bool)
		if !ok {
			return nil, fmt.Errorf("auto must be a bool, got %T", raw)
		}
		return s.applyAutoShim(ctx, on)
	}

	s.doCommandLock.Lock()
	defer s.doCommandLock.Unlock()

	var cmd cmdStruct
	err := mapstructure.Decode(cmdMap, &cmd)
	if err != nil {
		return nil, err
	}

	if cmd.Wipe {
		s.clearSquareCache()
		err := s.wipe(ctx)
		s.invalidateBoardCache()
		return nil, err
	}
	if cmd.ClearCache {
		s.clearSquareCache()
		return nil, nil
	}
	if cmd.Difficulty != 0 {
		applied, err := s.applyElo(cmd.Difficulty)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"difficulty": applied}, nil
	}
	if cmd.SetAnnounce != nil {
		s.announceEnabled.Store(*cmd.SetAnnounce)
		s.logger.Infof("announce set to %v", *cmd.SetAnnounce)
		return map[string]interface{}{"announce": *cmd.SetAnnounce}, nil
	}
	if cmd.BoardSnapshot {
		mode, auto, gameOver := s.modeFields()
		// Fast path: read the loop-populated cache; no per-call capture.
		s.boardCache.mu.RLock()
		if s.boardCache.ready {
			result := map[string]interface{}{
				"fen":             s.boardCache.fen,
				"camera_board":    s.boardCache.cameraBoard,
				"white_graveyard": s.boardCache.whiteGraveyard,
				"black_graveyard": s.boardCache.blackGraveyard,
				"auto":            auto,
				"mode":            mode,
				"game_over":       gameOver,
				"needs_fix":       s.boardCache.needsFix,
				"captured_at_ms":  s.boardCache.capturedAt.UnixMilli(),
				"event":           s.boardCache.gameEvents.Event,
				"outcome":         s.boardCache.gameEvents.Outcome,
				"method":          s.boardCache.gameEvents.Method,
				"turn":            s.boardCache.gameEvents.Turn,
				"in_check":        s.boardCache.gameEvents.InCheck,
				"is_over":         s.boardCache.gameEvents.IsOver,
				"score_cp":        s.boardCache.gameEvents.ScoreCP,
				"score_mate":      s.boardCache.gameEvents.ScoreMate,
			}
			s.boardCache.mu.RUnlock()
			return result, nil
		}
		s.boardCache.mu.RUnlock()
		// Cache empty (loop disabled or pre-first-tick) — capture inline.
		all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
		if err != nil {
			return nil, err
		}
		fen, cameraBoard, whiteGY, blackGY, events, err := s.buildSnapshotData(ctx, all)
		if err != nil {
			return nil, err
		}
		events.ScoreCP = int(s.lastScoreCP.Load())
		events.ScoreMate = int(s.lastScoreMate.Load())
		_ = s.refreshBoardCache(ctx, all)
		return map[string]interface{}{
			"fen":             fen,
			"camera_board":    cameraBoard,
			"white_graveyard": whiteGY,
			"black_graveyard": blackGY,
			"auto":            auto,
			"mode":            mode,
			"game_over":       gameOver,
			"needs_fix":       s.getNeedsFix(),
			"captured_at_ms":  time.Now().UnixMilli(),
			"event":           events.Event,
			"outcome":         events.Outcome,
			"method":          events.Method,
			"turn":            events.Turn,
			"in_check":        events.InCheck,
			"is_over":         events.IsOver,
			"score_cp":        events.ScoreCP,
			"score_mate":      events.ScoreMate,
		}, nil
	}

	if cmd.GameEvents {
		theState, err := s.getGame(ctx)
		if err != nil {
			return nil, err
		}
		result := gameEventsResult(theState.game)
		result.ScoreCP = int(s.lastScoreCP.Load())
		result.ScoreMate = int(s.lastScoreMate.Load())
		return result.Map(), nil
	}

	if cmd.CompanionConfig {
		return map[string]interface{}{
			"bad_state_delay_ms":    s.conf.companionBadStateDelayMs(),
			"welcome_revive_ms":     s.conf.companionWelcomeReviveMs(),
			"in_check_dismiss_ms":   s.conf.companionInCheckDismissMs(),
			"first_move_dismiss_ms": s.conf.companionFirstMoveDismissMs(),
		}, nil
	}

	if cmd.Hover != "" {
		err := s.goToStart(ctx)
		if err != nil {
			return nil, err
		}

		all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
		if err != nil {
			return nil, err
		}

		center, err := s.getCenterFor(all, cmd.Hover, nil)
		if err != nil {
			return nil, err
		}
		center.Z = max(15, center.Z) + 100

		err = s.setupGripper(ctx)
		if err != nil {
			return nil, err
		}

		err = s.moveGripper(ctx, center)
		if err != nil {
			return nil, err
		}

		return map[string]interface{}{"center": center}, nil
	}

	var videoFrom *time.Time
	var videoTags []string
	defer func() {
		err := s.goToStart(ctx)
		if err != nil {
			s.logger.Warnf("can't go home: %v", err)
		}
		if videoFrom != nil {
			s.saveVideo(ctx, *videoFrom, time.Now().UTC(), videoTags)
		}
		// Refresh cache so clients see post-command state without waiting
		// for the next loop tick.
		if all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil); err == nil {
			_ = s.refreshBoardCache(ctx, all)
		}
	}()

	if cmd.Move.To != "" && cmd.Move.From != "" {
		s.logger.Infof("move %v to %v", cmd.Move.From, cmd.Move.To)
		now := time.Now().UTC()
		videoFrom = &now
		videoTags = []string{"cmd=move", fmt.Sprintf("move=%s%s", cmd.Move.From, cmd.Move.To)}

		for x := range cmd.Move.N {
			err := s.goToStart(ctx)
			if err != nil {
				return nil, s.manualErr(err)
			}

			from, to := cmd.Move.From, cmd.Move.To
			if x%2 == 1 {
				to, from = from, to
			}
			all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
			if err != nil {
				return nil, s.manualErr(err)
			}

			err = s.movePiece(ctx, all, nil, from, to, nil, nil)
			if err != nil {
				return nil, s.manualErr(err)
			}
		}

		return nil, nil
	}

	if cmd.Go > 0 {
		now := time.Now().UTC()
		videoFrom = &now
		videoTags = []string{"cmd=go", fmt.Sprintf("go=%d", cmd.Go)}
		moves, err := s.makeNMoves(ctx, cmd.Go)
		for _, m := range moves {
			videoTags = append(videoTags, "move="+m.String())
		}
		if err != nil {
			return nil, s.manualErr(err)
		}
		last := moves[len(moves)-1]
		return map[string]interface{}{"move": last.String()}, nil
	}

	if cmd.Undo > 0 {
		return nil, s.manualErr(s.undoMoves(ctx, cmd.Undo))
	}

	if cmd.Reset {
		// Physical board reset (arm rearranges pieces) is the existing {reset:true}.
		// Never run it from ERROR — the arm may be unsafe (§6.4). The state-machine
		// reset is the separate {mode:0}.
		if s.mode.current() == ModeError {
			return nil, fmt.Errorf("physical reset is disabled in ERROR mode (arm may be unsafe); use {\"mode\":0} to reset state, or recover first")
		}
		return nil, s.manualErr(s.resetBoard(ctx))
	}

	if cmd.PlayFEN != "" {
		return nil, s.manualErr(s.playFENFile(ctx, cmd.PlayFEN))
	}

	return nil, fmt.Errorf("bad cmd %v", cmdMap)
}

// setMode applies the {"mode":N} command: a target-based transition validated
// against the table. Lock-free (§10.3) — it never takes doCommandLock, so a
// pause acks instantly while any in-flight arm motion finishes; the physical
// effect lands on the next tick.
func (s *viamChessChess) setMode(ctx context.Context, to Mode) (map[string]interface{}, error) {
	// Resuming from ERROR requires the saved game to be intact (§6.3).
	if s.mode.current() == ModeError && to != ModeStart {
		if _, err := s.getGame(ctx); err != nil {
			return nil, fmt.Errorf("cannot resume from ERROR: game state not intact: %w", err)
		}
	}
	from, err := s.mode.transition(to)
	if err != nil {
		return nil, err
	}
	// START -> active begins a fresh game. Safe to wipe inline: START has no
	// concurrent state writer, which also closes the boot-leftover race (§10.3).
	if from == ModeStart && (to == ModeVsHuman || to == ModeVsSelf) {
		if err := s.ensureNoGame(); err != nil {
			s.logger.Warnf("setMode %v->%v: ensureNoGame failed: %v", from, to, err)
		}
		s.logger.Infof("starting %v on a fresh game — assuming the physical board is set up at the starting position", to)
	}
	s.logger.Infof("mode %v -> %v", from, to)
	return map[string]interface{}{"mode": int(to)}, nil
}

// applyAutoShim maps the legacy {"auto":bool} command (still sent by the
// companion UI) onto mode transitions, so the unmodified UI keeps working (§10.6).
func (s *viamChessChess) applyAutoShim(ctx context.Context, on bool) (map[string]interface{}, error) {
	cur := s.mode.current()
	if on {
		switch cur {
		case ModeVsHuman:
			// already playing as black — no-op
		case ModeStart, ModeIdle:
			// start (from START) or resume (from IDLE when origin was VS_HUMAN)
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
	return map[string]interface{}{"auto": s.mode.current() == ModeVsHuman}, nil
}

// modeFields returns the mode-derived snapshot fields. auto = (mode==VS_HUMAN)
// is kept for backward compatibility alongside the new mode int.
func (s *viamChessChess) modeFields() (mode int, auto, gameOver bool) {
	ms := s.mode.snapshot()
	return int(ms.Mode), ms.Mode == ModeVsHuman, ms.GameOver
}

// getNeedsFix reads the needs_fix flag under the cache lock.
func (s *viamChessChess) getNeedsFix() bool {
	s.boardCache.mu.RLock()
	defer s.boardCache.mu.RUnlock()
	return s.boardCache.needsFix
}

// manualErr enters ERROR (the post-command defer homes the arm) when a manual
// gameplay command hit an execution fault; other errors pass through unchanged.
func (s *viamChessChess) manualErr(err error) error {
	if err != nil && isExecFailure(err) {
		s.logger.Errorf("manual command execution failure, entering ERROR: %v", err)
		s.mode.enterError()
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

const videoSaverTimeFormat = "2006-01-02_15-04-05"

// buildSnapshotData turns a camera capture + saved game state into the
// board-snapshot wire payload.
func (s *viamChessChess) buildSnapshotData(ctx context.Context, all viscapture.VisCapture) (
	fen string,
	cameraBoard map[string]interface{},
	whiteGY []interface{},
	blackGY []interface{},
	events GameEventsResult,
	err error,
) {
	theState, err := s.getGame(ctx)
	if err != nil {
		return
	}
	cameraBoard = map[string]interface{}{}
	for _, o := range all.Objects {
		label := o.Geometry.Label()
		if idx := strings.LastIndex(label, "-"); idx != -1 {
			cameraBoard[label[:idx]] = label[idx+1:]
		}
	}
	whiteGY = make([]interface{}, 0, len(theState.whiteGraveyard))
	for _, p := range theState.whiteGraveyard {
		if pStr := pieceIntToFEN(p); pStr != "" {
			whiteGY = append(whiteGY, pStr)
		}
	}
	blackGY = make([]interface{}, 0, len(theState.blackGraveyard))
	for _, p := range theState.blackGraveyard {
		if pStr := pieceIntToFEN(p); pStr != "" {
			blackGY = append(blackGY, pStr)
		}
	}
	fen = theState.game.FEN()
	events = gameEventsResult(theState.game)
	return
}

// refreshBoardCache rebuilds the snapshot cache from the given camera capture.
func (s *viamChessChess) refreshBoardCache(ctx context.Context, all viscapture.VisCapture) error {
	fen, cb, wg, bg, events, err := s.buildSnapshotData(ctx, all)
	if err != nil {
		return err
	}
	events.ScoreCP = int(s.lastScoreCP.Load())
	events.ScoreMate = int(s.lastScoreMate.Load())
	s.boardCache.mu.Lock()
	defer s.boardCache.mu.Unlock()
	s.boardCache.ready = true
	s.boardCache.fen = fen
	s.boardCache.cameraBoard = cb
	s.boardCache.whiteGraveyard = wg
	s.boardCache.blackGraveyard = bg
	s.boardCache.capturedAt = time.Now()
	s.boardCache.gameEvents = events
	return nil
}

// invalidateBoardCache marks the cache stale so the next reader re-captures.
// Used by wipe, which doesn't go through the post-command refresh defer.
func (s *viamChessChess) invalidateBoardCache() {
	s.boardCache.mu.Lock()
	defer s.boardCache.mu.Unlock()
	s.boardCache.ready = false
}

// GameEventsResult holds the current game-state events returned by the
// "game-events" DoCommand.
type GameEventsResult struct {
	// Event is the highest-priority active event: "checkmate", "stalemate",
	// "draw", "check", or "none".
	Event string `json:"event"`
	// Outcome is the game result: "in_progress", "white_won", "black_won", or "draw".
	Outcome string `json:"outcome"`
	// Method is how the game ended, or "none" while in progress: "checkmate",
	// "stalemate", "threefold_repetition", "fifty_move_rule",
	// "insufficient_material", "draw_offer", or "resignation".
	Method string `json:"method"`
	// Turn is whose move it is: "white" or "black".
	Turn string `json:"turn"`
	// InCheck is true when the side to move is currently in check (non-terminal).
	InCheck bool `json:"in_check"`
	// IsOver is true when the game has ended.
	IsOver bool `json:"is_over"`
	// ScoreCP is the engine evaluation in centipawns, white-relative.
	// Positive = white is ahead. 0 before the first engine move or when
	// no engine is configured.
	ScoreCP int `json:"score_cp"`
	// ScoreMate is the engine-detected moves to forced mate, white-relative.
	// Positive = white mates in N moves, negative = black mates in N moves,
	// 0 = no forced mate detected.
	ScoreMate int `json:"score_mate"`
}

// Map converts the result to the map[string]interface{} format required by DoCommand.
func (r GameEventsResult) Map() map[string]interface{} {
	return map[string]interface{}{
		"event":      r.Event,
		"outcome":    r.Outcome,
		"method":     r.Method,
		"turn":       r.Turn,
		"in_check":   r.InCheck,
		"is_over":    r.IsOver,
		"score_cp":   r.ScoreCP,
		"score_mate": r.ScoreMate,
	}
}

// gameEventsResult computes the current game-state events from a chess.Game.
// It is pure-read: no board mutations.
func gameEventsResult(game *chess.Game) GameEventsResult {
	outcome := game.Outcome()
	method := game.Method()

	// Detect check: the last played move carries the Check tag when it puts
	// the opponent (the side now to move) in check.
	inCheck := false
	if outcome == chess.NoOutcome {
		moves := game.Moves()
		if len(moves) > 0 {
			inCheck = moves[len(moves)-1].HasTag(chess.Check)
		}
	}

	var event string
	switch {
	case method == chess.Checkmate:
		event = "checkmate"
	case method == chess.Stalemate:
		event = "stalemate"
	case outcome == chess.Draw:
		event = "draw"
	case inCheck:
		event = "check"
	default:
		event = "none"
	}

	var outcomeStr string
	switch outcome {
	case chess.WhiteWon:
		outcomeStr = "white_won"
	case chess.BlackWon:
		outcomeStr = "black_won"
	case chess.Draw:
		outcomeStr = "draw"
	default:
		outcomeStr = "in_progress"
	}

	var methodStr string
	switch method {
	case chess.Checkmate:
		methodStr = "checkmate"
	case chess.Stalemate:
		methodStr = "stalemate"
	case chess.ThreefoldRepetition:
		methodStr = "threefold_repetition"
	case chess.FiftyMoveRule:
		methodStr = "fifty_move_rule"
	case chess.InsufficientMaterial:
		methodStr = "insufficient_material"
	case chess.DrawOffer:
		methodStr = "draw_offer"
	case chess.Resignation:
		methodStr = "resignation"
	default:
		methodStr = "none"
	}

	turn := "white"
	if game.Position().Turn() == chess.Black {
		turn = "black"
	}

	return GameEventsResult{
		Event:   event,
		Outcome: outcomeStr,
		Method:  methodStr,
		Turn:    turn,
		InCheck: inCheck,
		IsOver:  outcome != chess.NoOutcome,
	}
}

func (s *viamChessChess) saveVideo(ctx context.Context, from, to time.Time, tags []string) {
	if s.videoSaver == nil {
		return
	}
	_, err := s.videoSaver.DoCommand(ctx, map[string]interface{}{
		"command": "save",
		"from":    from.UTC().Format(videoSaverTimeFormat) + "Z",
		"to":      to.UTC().Format(videoSaverTimeFormat) + "Z",
		"tags":    tags,
		"async":   true,
	})
	if err != nil {
		s.logger.Warnf("video save failed: %v", err)
	}
}
