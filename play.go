package viamchess

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/corentings/chess/v2"
	"github.com/corentings/chess/v2/uci"

	"go.viam.com/rdk/vision/viscapture"
	"go.viam.com/utils/trace"
)

func (s *viamChessChess) pickMove(ctx context.Context, game *chess.Game) (*chess.Move, error) {
	ctx, span := trace.StartSpan(ctx, "pickMove")
	defer span.End()

	var picked *chess.Move
	if s.engine == nil {
		moves := game.ValidMoves()
		if len(moves) == 0 {
			return nil, fmt.Errorf("no valid moves")
		}
		picked = &moves[0]
	} else {
		cmdPos := uci.CmdPosition{Position: game.Position()}
		cmdGo := uci.CmdGo{MoveTime: time.Millisecond * time.Duration(s.conf.engineMillis())}
		err := s.engine.Run(cmdPos, cmdGo)
		if err != nil {
			return nil, errExec(err) // engine crash is an execution fault
		}
		sr := s.engine.SearchResults()
		picked = sr.BestMove

		// Normalize score to white-relative: UCI scores are from the side-to-move's
		// perspective, so negate when it was black's turn.
		cp := int32(sr.Info.Score.CP)
		mate := int32(sr.Info.Score.Mate)
		if game.Position().Turn() == chess.Black {
			cp = -cp
			if mate != 0 {
				mate = -mate
			}
		}
		s.lastScoreCP.Store(cp)
		s.lastScoreMate.Store(mate)
	}

	// Auto-queen.
	if picked.Promo() != chess.NoPieceType && picked.Promo() != chess.Queen {
		for _, vm := range game.ValidMoves() {
			if vm.S1() == picked.S1() && vm.S2() == picked.S2() && vm.Promo() == chess.Queen {
				vm := vm
				picked = &vm
				break
			}
		}
	}

	return picked, nil
}

func (s *viamChessChess) makeNMoves(ctx context.Context, n int) ([]*chess.Move, error) {
	moves := make([]*chess.Move, 0, n)
	for i := range n {
		m, err := s.makeAMove(ctx, i == 0)
		if err != nil {
			return moves, err
		}
		moves = append(moves, m)
	}
	return moves, nil
}

func (s *viamChessChess) makeAMove(ctx context.Context, doSanityCheck bool) (*chess.Move, error) {
	ctx, span := trace.StartSpan(ctx, "makeAMove")
	defer span.End()

	theState, err := s.getGame(ctx)
	if err != nil {
		return nil, err
	}

	// goToStart clears the arm from the camera's view; skip it once the square
	// cache is warm so the arm can move source-to-source.
	var all viscapture.VisCapture
	if !s.allSquaresCached() || doSanityCheck {
		err = s.goToStart(ctx)
		if err != nil {
			return nil, fmt.Errorf("can't go home: %v", err)
		}
		all, err = s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
		if err != nil {
			return nil, err
		}
		s.populateCacheFromCapture(all)
	}

	if doSanityCheck {
		_, err = s.checkPositionForMoves(ctx, all)
		if err != nil {
			return nil, err
		}
		// Reload: checkPositionForMoves may have applied & saved a human move.
		theState, err = s.getGame(ctx)
		if err != nil {
			return nil, err
		}
	}

	m, err := s.pickMove(ctx, theState.game)
	if err != nil {
		return nil, err
	}

	if m.HasTag(chess.KingSideCastle) || m.HasTag(chess.QueenSideCastle) {
		var f, t string
		switch m.S1().String() {
		case "e1":
			switch m.S2().String() {
			case "g1":
				f = "h1"
				t = "f1"
			case "c1":
				f = "a1"
				t = "d1"
			default:
				return nil, fmt.Errorf("bad castle? %v", m)
			}
		case "e8":
			switch m.S2().String() {
			case "g8":
				f = "h8"
				t = "f8"
			case "c8":
				f = "a8"
				t = "d8"
			default:
				return nil, fmt.Errorf("bad castle? %v", m)
			}
		default:
			return nil, fmt.Errorf("bad castle? %v", m)
		}

		err = s.movePiece(ctx, all, theState, f, t, nil, nil)
		if err != nil {
			return nil, err
		}
	}

	if m.HasTag(chess.EnPassant) {
		startRank := m.S1().String()[1]
		endFile := m.S2().String()[0]

		pieceToRemoveSquare := fmt.Sprintf("%c%c", endFile, startRank)
		err = s.movePiece(ctx, all, theState, pieceToRemoveSquare, "-", nil, nil)
		if err != nil {
			return nil, err
		}

		if startRank == '5' {
			theState.blackGraveyard = append(theState.blackGraveyard, 12)
		} else {
			theState.whiteGraveyard = append(theState.whiteGraveyard, 6)
		}
	}

	if m.Promo() != chess.NoPieceType {
		if err := s.handlePromotionMove(ctx, all, theState, m); err != nil {
			return nil, err
		}
	} else {
		err = s.movePiece(ctx, all, theState, m.S1().String(), m.S2().String(), m, nil)
		if err != nil {
			return nil, err
		}
	}

	err = theState.game.Move(m, nil)
	if err != nil {
		return nil, err
	}

	err = s.saveGame(ctx, theState)
	if err != nil {
		return nil, err
	}

	s.announceMove(m.String(), theState.game.FEN(), "engine")

	return m, nil
}

// announceMove dispatches a "move_made" event to the configured on_move_target
// generic service. Fires after every engine move (cmd.Go and auto-mode both
// land here via makeAMove). Fire-and-forget: a goroutine with its own timeout
// so a slow or broken downstream cannot delay the chess move.
func (s *viamChessChess) announceMove(move, fen, by string) {
	if !s.announceEnabled.Load() || s.onMoveTarget == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		payload := map[string]interface{}{
			"event": "move_made",
			"move":  move,
			"fen":   fen,
			"by":    by,
		}
		if _, err := s.onMoveTarget.DoCommand(ctx, payload); err != nil {
			s.logger.Warnf("on_move_target dispatch failed: %v", err)
		}
	}()
}

func (s *viamChessChess) undoMoves(ctx context.Context, n int) error {
	ctx, span := trace.StartSpan(ctx, "undoMoves")
	defer span.End()

	theState, err := s.getGame(ctx)
	if err != nil {
		return err
	}

	moves := theState.game.Moves()
	if n > len(moves) {
		return fmt.Errorf("cannot undo %d moves: only %d have been played", n, len(moves))
	}

	keepCount := len(moves) - n

	for i := keepCount; i < len(moves); i++ {
		if moves[i].Promo() != chess.NoPieceType {
			return fmt.Errorf("cannot undo through promotion move %s", moves[i].String())
		}
	}

	err = s.goToStart(ctx)
	if err != nil {
		return fmt.Errorf("can't go home: %w", err)
	}
	all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
	if err != nil {
		return err
	}
	s.populateCacheFromCapture(all)

	// Replay history to recover what each move captured (for graveyard restoration)
	// and what landed on m.S2() (handles promotion).
	type moveInfo struct {
		capturedPiece chess.Piece
		captureSquare string
		movedPiece    chess.Piece
	}
	infos := make([]moveInfo, len(moves))
	tempGame := chess.NewGame()
	for i, m := range moves {
		board := tempGame.Position().Board()
		info := moveInfo{}
		if m.HasTag(chess.EnPassant) {
			startRank := m.S1().String()[1]
			endFile := m.S2().String()[0]
			info.captureSquare = fmt.Sprintf("%c%c", endFile, startRank)
			if startRank == '5' {
				info.capturedPiece = chess.BlackPawn
			} else {
				info.capturedPiece = chess.WhitePawn
			}
		} else if m.HasTag(chess.Capture) {
			info.capturedPiece = board.Piece(m.S2())
			info.captureSquare = m.S2().String()
		}
		if err := tempGame.Move(m, nil); err != nil {
			return fmt.Errorf("replay move %d: %w", i, err)
		}
		info.movedPiece = tempGame.Position().Board().Piece(m.S2())
		infos[i] = info
	}

	curWhiteGY := make([]int, len(theState.whiteGraveyard))
	copy(curWhiteGY, theState.whiteGraveyard)
	curBlackGY := make([]int, len(theState.blackGraveyard))
	copy(curBlackGY, theState.blackGraveyard)

	// Stamp the snapshot empty so subsequent movePiece calls don't see stale occupancy.
	clearSquare := func(squareName string) {
		for _, o := range all.Objects {
			if strings.HasPrefix(o.Geometry.Label(), squareName+"-") {
				o.Geometry.SetLabel(squareName + "-0")
				break
			}
		}
	}

	for i := len(moves) - 1; i >= keepCount; i-- {
		m := moves[i]
		info := infos[i]

		// theState/board are nil (camera-driven occupancy), so auto-detect can't
		// see what's on m.S2(); pass the replayed piece's pickup-Z explicitly.
		if err := s.movePieceWithPickupZ(ctx, all, nil, m.S2().String(), m.S1().String(), nil, nil, s.pickupZForPieceType(info.movedPiece.Type())); err != nil {
			return fmt.Errorf("undo move %s: %w", m.String(), err)
		}
		clearSquare(m.S2().String())

		if m.HasTag(chess.KingSideCastle) || m.HasTag(chess.QueenSideCastle) {
			var rookFrom, rookTo string
			switch m.S1().String() {
			case "e1":
				if m.HasTag(chess.KingSideCastle) {
					rookFrom, rookTo = "f1", "h1"
				} else {
					rookFrom, rookTo = "d1", "a1"
				}
			case "e8":
				if m.HasTag(chess.KingSideCastle) {
					rookFrom, rookTo = "f8", "h8"
				} else {
					rookFrom, rookTo = "d8", "a8"
				}
			}
			if err := s.movePiece(ctx, all, nil, rookFrom, rookTo, nil, nil); err != nil {
				return fmt.Errorf("undo castle rook: %w", err)
			}
			clearSquare(rookFrom)
		}

		if info.capturedPiece != chess.NoPiece {
			isWhite := info.capturedPiece.Color() == chess.White
			var gyFrom string
			if isWhite {
				idx := len(curWhiteGY) - 1
				// Physical slot is slice idx + 1 (slot 0 is the spare queen).
				gyFrom = fmt.Sprintf("XW%d", idx+1)
				curWhiteGY = curWhiteGY[:idx]
			} else {
				idx := len(curBlackGY) - 1
				gyFrom = fmt.Sprintf("XB%d", idx+1)
				curBlackGY = curBlackGY[:idx]
			}
			// Source is a graveyard slot, so force pickup-Z.
			if err := s.movePieceWithPickupZ(ctx, all, nil, gyFrom, info.captureSquare, nil, nil, s.pickupZForPieceType(info.capturedPiece.Type())); err != nil {
				return fmt.Errorf("undo restore captured piece: %w", err)
			}
		}
	}

	// Rebuild state by replaying the kept moves, re-deriving the graveyard.
	newState := &state{game: chess.NewGame(), whiteGraveyard: []int{}, blackGraveyard: []int{}}
	for i := 0; i < keepCount; i++ {
		m := moves[i]
		board := newState.game.Position().Board()
		if m.HasTag(chess.EnPassant) {
			startRank := m.S1().String()[1]
			if startRank == '5' {
				newState.blackGraveyard = append(newState.blackGraveyard, int(chess.BlackPawn))
			} else {
				newState.whiteGraveyard = append(newState.whiteGraveyard, int(chess.WhitePawn))
			}
		} else if m.HasTag(chess.Capture) {
			captured := board.Piece(m.S2())
			if captured.Color() == chess.White {
				newState.whiteGraveyard = append(newState.whiteGraveyard, int(captured))
			} else {
				newState.blackGraveyard = append(newState.blackGraveyard, int(captured))
			}
		}
		// Reusing the original *chess.Move would drag along its children from
		// the old tree, so newState.Moves() would traverse the undone tail.
		if err := newState.game.PushNotationMove(m.String(), chess.UCINotation{}, nil); err != nil {
			return fmt.Errorf("rebuild move %d: %w", i, err)
		}
	}

	// Some piece finders cache the last capture; refresh so the next `go` doesn't
	// see N×2 spurious diffs from a pre-undo snapshot.
	s.clearSquareCache()
	if err := s.goToStart(ctx); err != nil {
		return fmt.Errorf("can't go home after undo: %w", err)
	}
	allFresh, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
	if err != nil {
		return fmt.Errorf("can't refresh snapshot after undo: %w", err)
	}
	s.populateCacheFromCapture(allFresh)

	return s.saveGame(ctx, newState)
}

// Detects, applies, and saves an unregistered human move. (nil, nil) = no diff.
func (s *viamChessChess) checkPositionForMoves(ctx context.Context, all viscapture.VisCapture) (*chess.Move, error) {
	ctx, span := trace.StartSpan(ctx, "checkPositionForMoves")
	defer span.End()

	theState, err := s.getGame(ctx)
	if err != nil {
		return nil, err
	}

	var differences []chess.Square
	from := chess.NoSquare
	to := chess.NoSquare

	// Recapture on weird diffs (vision noise); a real illegal state will persist
	// past badDiffMaxAttempts and surface as an error.
	badDiffMaxAttempts := s.conf.badDiffMaxAttempts()
	for attempt := 1; ; attempt++ {
		differences = differences[:0]
		from = chess.NoSquare
		to = chess.NoSquare

		for sq := chess.A1; sq <= chess.H8; sq++ {
			x := squareToString(sq)

			fromState := theState.game.Position().Board().Piece(sq)
			o := s.findObject(all, x)
			if o == nil {
				return nil, fmt.Errorf("can't find object for square %s during position check", x)
			}
			oc := int(o.Geometry.Label()[3] - '0')

			if int(fromState.Color()) != oc {
				s.logger.Infof("different %s fromState: %v o: %v oc: %v", x, fromState, o.Geometry.Label(), oc)
				differences = append(differences, sq)
				if oc == 0 {
					from = sq
				} else if oc > 0 {
					to = sq
				}
			}
		}

		if len(differences) == 0 {
			return nil, nil
		}

		if len(differences) == 4 {
			// castle?
			if squaresSame(differences, []chess.Square{chess.E1, chess.F1, chess.G1, chess.H1}) {
				from, to, differences = chess.E1, chess.G1, nil
			} else if squaresSame(differences, []chess.Square{chess.E1, chess.A1, chess.C1, chess.D1}) {
				from, to, differences = chess.E1, chess.C1, nil
			} else if squaresSame(differences, []chess.Square{chess.E8, chess.F8, chess.G8, chess.H8}) {
				from, to, differences = chess.E8, chess.G8, nil
			} else if squaresSame(differences, []chess.Square{chess.E8, chess.A8, chess.C8, chess.D8}) {
				from, to, differences = chess.E8, chess.C8, nil
			}
		}

		if len(differences) == 0 {
			break
		}
		if len(differences) == 2 && !isCastleSquarePair(from, to) {
			break
		}

		if attempt >= badDiffMaxAttempts {
			return nil, fmt.Errorf("bad number of differences (%d) after %d attempts: %v", len(differences), attempt, differences)
		}

		s.logger.Warnf("bad number of differences (%d) on attempt %d: %v — recapturing", len(differences), attempt, differences)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		time.Sleep(200 * time.Millisecond)
		fresh, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
		if err != nil {
			return nil, fmt.Errorf("recapture after bad differences (attempt %d): %w", attempt, err)
		}
		all = fresh
	}

	moves := theState.game.ValidMoves()
	for _, m := range moves {
		if m.S1() == from && m.S2() == to {
			s.logger.Infof("found it: %v", m.String())

			// Record human-placed captures so reset can retrieve them.
			if m.HasTag(chess.Capture) {
				captured := theState.game.Position().Board().Piece(m.S2())
				if captured != chess.NoPiece {
					if captured.Color() == chess.White {
						theState.whiteGraveyard = append(theState.whiteGraveyard, int(captured))
					} else {
						theState.blackGraveyard = append(theState.blackGraveyard, int(captured))
					}
				}
			} else if m.HasTag(chess.EnPassant) {
				if m.S1().Rank() == chess.Rank5 {
					theState.blackGraveyard = append(theState.blackGraveyard, int(chess.BlackPawn))
				} else {
					theState.whiteGraveyard = append(theState.whiteGraveyard, int(chess.WhitePawn))
				}
			}

			err = theState.game.Move(&m, nil)
			if err != nil {
				return nil, err
			}

			// Human performs the physical pawn→queen swap themselves; record
			// the vanished pawn so reset/snapshot stay consistent.
			if m.Promo() != chess.NoPieceType {
				if m.S2().Rank() == chess.Rank8 {
					theState.whiteGraveyard = append(theState.whiteGraveyard, int(chess.WhitePawn))
				} else {
					theState.blackGraveyard = append(theState.blackGraveyard, int(chess.BlackPawn))
				}
			}

			err = s.saveGame(ctx, theState)
			if err != nil {
				return nil, err
			}

			s.announceMove(m.String(), theState.game.FEN(), "human")

			return &m, nil
		}
	}

	return nil, fmt.Errorf("no valid moves from: %s to %s found out of %d", squareToString(from), squareToString(to), len(moves))
}

// isCastleSquarePair reports whether (from, to) is the king's from/to for any
// of the four castling moves. Used to defer move resolution while a human is
// mid-castle: after the king has moved but before the rook has, the camera sees
// only 2 diffs and the engine would otherwise commit the move as a castle and
// race the human to the rook.
func isCastleSquarePair(from, to chess.Square) bool {
	switch {
	case from == chess.E1 && to == chess.G1:
		return true
	case from == chess.E1 && to == chess.C1:
		return true
	case from == chess.E8 && to == chess.G8:
		return true
	case from == chess.E8 && to == chess.C8:
		return true
	}
	return false
}

func squaresSame(a, b []chess.Square) bool {
	if len(a) != len(b) {
		return false
	}

	for _, sq := range a {
		found := false
		if slices.Contains(b, sq) {
			found = true
			break
		}
		if !found {
			return false
		}
	}
	return true
}

// runBoardLoop captures the camera each tick, registers any human ply via
// checkPositionForMoves, refreshes boardCache, and (when `auto` is on and
// it's black to move) plays the engine reply. Holds doCommandLock per tick
// so manual commands interleave.
func (s *viamChessChess) runBoardLoop(ctx context.Context) {
	interval := s.conf.boardLoopInterval()
	if interval == 0 {
		s.logger.Info("board loop disabled by config (board-loop-interval-ms=0)")
		return
	}
	s.logger.Infof("board loop starting at %v cadence", interval)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.boardTick(ctx)
		}
	}
}

func (s *viamChessChess) boardTick(ctx context.Context) {
	ctx, span := trace.StartSpan(ctx, "boardTick")
	defer span.End()

	// Read the mode once at tick start. A lock-free mode change mid-tick takes
	// effect on the NEXT tick; the in-flight arm motion below finishes first (§6.1).
	mode := s.mode.current()
	s.logger.Debugf("boardTick: mode=%v", mode) // per-tick; transitions log at Info

	// ERROR halts the loop: do nothing until resume/reset (§6.3). Returning
	// before taking doCommandLock keeps manual recovery commands responsive.
	if mode == ModeError {
		return
	}

	s.doCommandLock.Lock()
	defer s.doCommandLock.Unlock()

	// START enforces "no game" so START -> active always begins fresh (§7.1, §10.3).
	if mode == ModeStart {
		if err := s.ensureNoGame(); err != nil {
			s.logger.Warnf("boardTick START: ensureNoGame failed: %v", err)
		}
	}

	// Shared prologue for every live mode: home the arm (clears the camera),
	// capture, warm the coord cache. A homing/capture failure here is transient,
	// not an execution fault — skip the tick (§10.4).
	if err := s.goToStart(ctx); err != nil {
		s.logger.Warnf("boardTick: goToStart failed (loop tick lost): %v", err)
		return
	}
	all, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil)
	if err != nil {
		s.logger.Warnf("boardTick: capture failed (loop tick lost): %v", err)
		return
	}
	s.populateCacheFromCapture(all)

	switch mode {
	case ModeStart, ModeIdle, ModeTeaching:
		// Passive: refresh the live snapshot only; never move a piece (§5).
		s.setNeedsFix(false, "")
		if err := s.refreshBoardCache(ctx, all); err != nil {
			s.logger.Warnf("boardTick: cache refresh failed: %v", err)
		}
	case ModeVsHuman:
		s.tickVsHuman(ctx, all)
	case ModeVsSelf:
		s.tickVsSelf(ctx, all)
	}
}

// tickVsHuman detects the human's ply and replies as black. This is the former
// "auto" behavior, now gated by mode.
func (s *viamChessChess) tickVsHuman(ctx context.Context, all viscapture.VisCapture) {
	// Detect any human ply. A detection error means the board can't be mapped to
	// a legal move (illegal/scrambled, or a transient capture hiccup): surface
	// needs_fix and wait for a human — never ERROR (§7.4).
	m, err := s.checkPositionForMoves(ctx, all)
	if err != nil {
		s.setNeedsFix(true, err.Error())
	} else {
		s.setNeedsFix(false, "")
		if m != nil {
			s.logger.Infof("VS_HUMAN: detected human move %s -> %s", m.S1().String(), m.S2().String())
		}
	}

	if err := s.refreshBoardCache(ctx, all); err != nil {
		s.logger.Warnf("boardTick: cache refresh failed: %v", err)
	}

	// Game-over check BEFORE replying: a human-delivered mate must not trigger a
	// doomed makeAMove (§10.5).
	if s.checkGameOver(ctx) {
		return
	}

	// Reply only when a human move was just registered and it's black's turn.
	if m == nil {
		return
	}
	theState, err := s.getGame(ctx)
	if err != nil {
		s.logger.Warnf("VS_HUMAN: getGame failed: %v", err)
		return
	}
	if theState.game.Position().Turn() != chess.Black {
		return // robot plays black only (§6.6)
	}
	if _, err := s.makeAMove(ctx, false); err != nil {
		s.handleMoveErr(ctx, "VS_HUMAN reply", err)
		return
	}
	// Refresh so the post-reply state lands without waiting a tick.
	if all2, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil); err == nil {
		_ = s.refreshBoardCache(ctx, all2)
	}
	// The reply itself may have ended the game (e.g. engine delivers mate).
	s.checkGameOver(ctx)
}

// tickVsSelf plays one blind ply per tick, alternating colors. No per-move
// vision verification — it trusts the arm (§7.3).
func (s *viamChessChess) tickVsSelf(ctx context.Context, all viscapture.VisCapture) {
	if err := s.refreshBoardCache(ctx, all); err != nil {
		s.logger.Warnf("boardTick: cache refresh failed: %v", err)
	}

	// Defensive: if the game already ended, park — no auto-restart (§7.3).
	if s.checkGameOver(ctx) {
		s.setNeedsFix(false, "")
		return
	}

	theState, err := s.getGame(ctx)
	if err != nil {
		s.logger.Warnf("VS_SELF: getGame failed: %v", err)
		return
	}
	// Verify the board only on the first ply of the game, reusing makeAMove's
	// existing sanity check exactly as cmd.Go does for its first move. After that,
	// self-play is blind and trusts the arm (§7.3).
	firstPly := len(theState.game.Moves()) == 0

	if _, err := s.makeAMove(ctx, firstPly); err != nil {
		// On the first ply a non-exec error means the board isn't set up at the
		// starting position yet — wait and prompt (needs_fix), don't fault.
		if firstPly && !isExecFailure(err) {
			s.setNeedsFix(true, err.Error())
			return
		}
		s.handleMoveErr(ctx, "VS_SELF move", err)
		return
	}
	s.setNeedsFix(false, "")

	if all2, err := s.pieceFinder.CaptureAllFromCamera(ctx, "", viscapture.CaptureOptions{}, nil); err == nil {
		_ = s.refreshBoardCache(ctx, all2)
	}
	s.checkGameOver(ctx)
}

// handleMoveErr routes a failed move: execution faults (arm/gripper/engine) halt
// into ERROR and home the arm; anything else (non-exec) is logged and skipped.
func (s *viamChessChess) handleMoveErr(ctx context.Context, where string, err error) {
	if isExecFailure(err) {
		s.logger.Errorf("%s: execution failure, entering ERROR: %v", where, err)
		s.enterErrorAndHome(ctx)
		return
	}
	s.logger.Warnf("%s failed (non-exec, staying in mode): %v", where, err)
}

// enterErrorAndHome records the fault, halts into ERROR, and homes the arm
// best-effort. state.json is preserved (§6.3).
func (s *viamChessChess) enterErrorAndHome(ctx context.Context) {
	s.mode.enterError()
	if err := s.goToStart(ctx); err != nil {
		s.logger.Warnf("enterError: best-effort home failed: %v", err)
	}
}

// checkGameOver parks in IDLE with resume disabled if the saved game has ended,
// returning true. Only modes 2/3 call this.
func (s *viamChessChess) checkGameOver(ctx context.Context) bool {
	theState, err := s.getGame(ctx)
	if err != nil {
		s.logger.Warnf("game-over check: getGame failed: %v", err)
		return false
	}
	if theState.game.Outcome() != chess.NoOutcome {
		s.logger.Infof("game over (%v / %v) -> IDLE", theState.game.Outcome(), theState.game.Method())
		s.mode.enterIdle(true)
		return true
	}
	return false
}

// setNeedsFix records whether the human board currently can't be resolved to a
// legal move; surfaced as needs_fix in the snapshot. Logs only on a state change
// so a persistently-bad (or persistently-fine) board doesn't spam the loop.
func (s *viamChessChess) setNeedsFix(v bool, reason string) {
	s.boardCache.mu.Lock()
	changed := s.boardCache.needsFix != v
	s.boardCache.needsFix = v
	s.boardCache.mu.Unlock()
	if !changed {
		return
	}
	if v {
		s.logger.Warnf("needs_fix SET: board does not match the game state — waiting for a human to fix the board (%s)", reason)
	} else {
		s.logger.Infof("needs_fix CLEARED: board matches the game state again")
	}
}
