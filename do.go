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
	Skill           float64
	Hover           string
	ClearCache      bool `mapstructure:"clear-cache"`
	Undo            int
	PlayFEN         string `mapstructure:"play-fen"`
	BoardSnapshot   bool   `mapstructure:"board-snapshot"`
	GameEvents      bool   `mapstructure:"game-events"`
	CompanionConfig bool   `mapstructure:"companion-config"`
	Auto            *bool  // pointer so explicit false is distinguishable from absent
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
		s.boardCache.mu.RLock()
		if s.boardCache.ready {
			result := map[string]interface{}{
				"fen":             s.boardCache.fen,
				"camera_board":    s.boardCache.cameraBoard,
				"white_graveyard": s.boardCache.whiteGraveyard,
				"black_graveyard": s.boardCache.blackGraveyard,
				"auto":            s.autoEnabled.Load(),
				"skill":           s.skillAdjust,
				"captured_at_ms":  s.boardCache.capturedAt.UnixMilli(),
				"event":           s.boardCache.gameEvents.Event,
				"outcome":         s.boardCache.gameEvents.Outcome,
				"method":          s.boardCache.gameEvents.Method,
				"turn":            s.boardCache.gameEvents.Turn,
				"in_check":        s.boardCache.gameEvents.InCheck,
				"is_over":         s.boardCache.gameEvents.IsOver,
			}
			s.boardCache.mu.RUnlock()
			return result, nil
		}
		s.boardCache.mu.RUnlock()
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
	if cmd.Skill > 0 {
		s.skillAdjust = cmd.Skill
		return nil, nil
	}
	if cmd.Auto != nil {
		s.autoEnabled.Store(*cmd.Auto)
		return map[string]interface{}{"auto": *cmd.Auto}, nil
	}
	if cmd.SetAnnounce != nil {
		s.announceEnabled.Store(*cmd.SetAnnounce)
		s.logger.Infof("announce set to %v", *cmd.SetAnnounce)
		return map[string]interface{}{"announce": *cmd.SetAnnounce}, nil
	}
	if cmd.BoardSnapshot {
		// Fast path: read the loop-populated cache; no per-call capture.
		s.boardCache.mu.RLock()
		if s.boardCache.ready {
			result := map[string]interface{}{
				"fen":             s.boardCache.fen,
				"camera_board":    s.boardCache.cameraBoard,
				"white_graveyard": s.boardCache.whiteGraveyard,
				"black_graveyard": s.boardCache.blackGraveyard,
				"auto":            s.autoEnabled.Load(),
				"skill":           s.skillAdjust,
				"captured_at_ms":  s.boardCache.capturedAt.UnixMilli(),
				"event":           s.boardCache.gameEvents.Event,
				"outcome":         s.boardCache.gameEvents.Outcome,
				"method":          s.boardCache.gameEvents.Method,
				"turn":            s.boardCache.gameEvents.Turn,
				"in_check":        s.boardCache.gameEvents.InCheck,
				"is_over":         s.boardCache.gameEvents.IsOver,
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
		_ = s.refreshBoardCache(ctx, all)
		return map[string]interface{}{
			"fen":             fen,
			"camera_board":    cameraBoard,
			"white_graveyard": whiteGY,
			"black_graveyard": blackGY,
			"auto":            s.autoEnabled.Load(),
			"skill":           s.skillAdjust,
			"captured_at_ms":  time.Now().UnixMilli(),
			"event":           events.Event,
			"outcome":         events.Outcome,
			"method":          events.Method,
			"turn":            events.Turn,
			"in_check":        events.InCheck,
			"is_over":         events.IsOver,
		}, nil
	}

	if cmd.GameEvents {
		theState, err := s.getGame(ctx)
		if err != nil {
			return nil, err
		}
		return gameEventsResult(theState.game).Map(), nil
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
				return nil, err
			}

			from, to := cmd.Move.From, cmd.Move.To
			if x%2 == 1 {
				to, from = from, to
			}
			all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
			if err != nil {
				return nil, err
			}

			err = s.movePiece(ctx, all, nil, from, to, nil, nil)
			if err != nil {
				return nil, err
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
			return nil, err
		}
		last := moves[len(moves)-1]
		return map[string]interface{}{"move": last.String()}, nil
	}

	if cmd.Undo > 0 {
		err = s.undoMoves(ctx, cmd.Undo)
		return nil, err
	}

	if cmd.Reset {
		return nil, s.resetBoard(ctx)
	}

	if cmd.PlayFEN != "" {
		return nil, s.playFENFile(ctx, cmd.PlayFEN)
	}

	return nil, fmt.Errorf("bad cmd %v", cmdMap)
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
}

// Map converts the result to the map[string]interface{} format required by DoCommand.
func (r GameEventsResult) Map() map[string]interface{} {
	return map[string]interface{}{
		"event":    r.Event,
		"outcome":  r.Outcome,
		"method":   r.Method,
		"turn":     r.Turn,
		"in_check": r.InCheck,
		"is_over":  r.IsOver,
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
