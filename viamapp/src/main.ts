import { createRobotClient, GenericServiceClient, StreamClient } from "@viamrobotics/sdk";
import { Struct, type JsonValue } from "@viamrobotics/sdk";
import type { RobotClient } from "@viamrobotics/sdk";
import Cookies from "js-cookie";
import * as companion from "./companion";
import type { GameOutcome } from "./companion";

// ── Types ──────────────────────────────────────────────────────────────────

interface MachineCookie {
  apiKey: { id: string; key: string };
  hostname: string;
  machineName?: string;
}

type EvtType = "go" | "undo" | "reset" | "wipe" | "cache" | "refresh" | "snapshot" | "err" | "mode";
interface MoveEntry {
  kind: "move";
  i: number;
  san: string;
  from: string;
  to: string;
  color: "w" | "b";
}
interface EvtEntry {
  kind: "evt";
  type: EvtType;
  label: string;
}
type TapeEntry = MoveEntry | EvtEntry;

type Mismatch = { sq: string; kind: "missing" | "phantom" | "wrongcolor" };

// ── Piece SVGs ─────────────────────────────────────────────────────────────

const pieceSvgs = import.meta.glob("../pieces/*.svg", {
  eager: true,
  query: "?url",
  import: "default",
}) as Record<string, string>;

const PIECE_FILE: Record<string, string> = {
  K: "white-king", Q: "white-queen", R: "white-rook",
  B: "white-bishop", N: "white-knight", P: "white-pawn",
  k: "black-king", q: "black-queen", r: "black-rook",
  b: "black-bishop", n: "black-knight", p: "black-pawn",
};

function pieceUrl(piece: string): string | undefined {
  const name = PIECE_FILE[piece];
  if (!name) return undefined;
  return pieceSvgs[`../pieces/${name}.svg`];
}

const PIECE_VALUE: Record<string, number> = { P: 1, N: 3, B: 3, R: 5, Q: 9, K: 0 };

// ── State ──────────────────────────────────────────────────────────────────

const CHESS_SERVICE_NAME = "chess";
const CAMERA_NAME = "cam-above";

let chessService: GenericServiceClient | null = null;
let robotClient: RobotClient | null = null;
let camStream: MediaStream | null = null;
let camStreamClient: StreamClient | null = null;
let camAttachInFlight = false;
let currentFen: string | null = null;
let currentBoard: (string | null)[][] = emptyBoard();
let currentTurn: "w" | "b" = "w";
let cameraBoard: Record<string, string> | null = null;
let mismatches: Mismatch[] = [];
let whiteGraveyard: string[] = [];
let blackGraveyard: string[] = [];
let lastMove: { from: string; to: string; san?: string } | null = null;

let tapeItems: TapeEntry[] = [];
let plyCount = 0;
let showTapeLogs = true;
let initialLoaded = false;

// When the user makes a direct move, we apply it optimistically and the UI
// becomes authoritative for board state. Server snapshots during this window
// still update camera/mismatches, but not the board — otherwise a stale server
// FEN reverts the move in the UI while the camera already shows the new position.
// Robot `go` / `wipe` / `reset` flip this back to true so the server drives state.
// In VS_HUMAN the hand-back is snapshot-driven instead (see pendingMovePly).
let serverAuthoritative = true;

// VS_HUMAN drag-move flow: the UI only actuates the arm ({move}); the server's
// board loop registers the ply and replies, exactly as if the human had moved
// the piece by hand. This holds the target ply count until the server catches
// up, at which point snapshots become authoritative again.
let pendingMovePly: number | null = null;

// After cmdUndo runs, the next FEN diff will look like a piece moving back to
// its source square. This flag tells applySnapshot to ignore that one inference
// so it doesn't re-push the undone move into the tape as a phantom forward move.
let suppressFenInferOnce = false;

let currentScoreCP = 0;
let currentScoreMate = 0;

let selectedSq: string | null = null;
let autoRefreshTimer: ReturnType<typeof setInterval> | null = null;
let refreshInFlight = false;
let busy = false;

// The robot runs a 6-mode state machine (mode.go); every DoCommand response
// carries the current mode and board-snapshot adds game_over/needs_fix. We
// mirror them locally to drive the mode control, gate gameplay commands, and
// show the fault banner. The server is always the source of truth.
const MODE_START = 0;
const MODE_IDLE = 1;
const MODE_VS_HUMAN = 2;
const MODE_VS_SELF = 3;
const MODE_TEACHING = 4;
const MODE_ERROR = 5;
const MODE_NAMES = ["START", "IDLE", "VS_HUMAN", "VS_SELF", "TEACHING", "ERROR"];

let currentMode = MODE_START;
let modeKnown = false; // false until the first server response carries a mode
let gameOver = false;
let needsFix = false;
// Populated from {"mode-status": true} when we observe IDLE/ERROR, so Resume
// knows its target. 0 (START) means "unknown" — START is never a resume target.
let idleOrigin = 0;
let errPrevMode = 0;

function gameActive(): boolean {
  return currentMode === MODE_VS_HUMAN || currentMode === MODE_VS_SELF || currentMode === MODE_TEACHING;
}
// Active vs idle snapshot cadence. Snapshot reads from a server-side cache
// (no per-call camera capture), so this is purely about UI freshness.
const ACTIVE_REFRESH_MS = 500;
const IDLE_POLL_MS = 10000;
const IDLE_THRESHOLD_MS = 5 * 60 * 1000;
let refreshPollMs = ACTIVE_REFRESH_MS;
let idleMode = false;
let lastActivityAt = Date.now();

// ── Helpers ────────────────────────────────────────────────────────────────

function emptyBoard(): (string | null)[][] {
  return Array.from({ length: 8 }, () => Array(8).fill(null));
}

function plyCountFromFEN(fen: string): number {
  const parts = fen.split(" ");
  const turn = parts[1] ?? "w";
  const fullMove = parseInt(parts[5] ?? "1", 10) || 1;
  return (fullMove - 1) * 2 + (turn === "b" ? 1 : 0);
}

function parseFENPlacement(fen: string): { board: (string | null)[][]; turn: "w" | "b" } {
  const [placement, turn] = fen.split(" ");
  const rows = placement.split("/");
  const board = rows.map((row) => {
    const out: (string | null)[] = [];
    for (const ch of row) {
      if (/\d/.test(ch)) for (let i = 0; i < parseInt(ch, 10); i++) out.push(null);
      else out.push(ch);
    }
    return out;
  });
  while (board.length < 8) board.push(Array(8).fill(null));
  return { board, turn: turn === "b" ? "b" : "w" };
}

function rcToSq(r: number, c: number): string {
  return String.fromCharCode(97 + c) + String(8 - r);
}

function diffCamera(board: (string | null)[][], cam: Record<string, string> | null): Mismatch[] {
  if (!cam) return [];
  const out: Mismatch[] = [];
  for (let r = 0; r < 8; r++) {
    for (let c = 0; c < 8; c++) {
      const sq = rcToSq(r, c);
      const p = board[r]?.[c] ?? null;
      const expected = p ? (p === p.toUpperCase() ? "white" : "black") : null;
      const reading = cam[sq] ?? "0";
      const detected = reading === "1" ? "white" : reading === "2" ? "black" : null;
      if (expected !== detected) {
        const kind: Mismatch["kind"] =
          expected && !detected ? "missing" : !expected && detected ? "phantom" : "wrongcolor";
        out.push({ sq, kind });
      }
    }
  }
  return out;
}

function sumValue(pieces: string[]): number {
  return pieces.reduce((a, p) => a + (PIECE_VALUE[p.toUpperCase()] ?? 0), 0);
}

// Infer a single-ply move from a before/after board diff. Returns null when
// the diff doesn't cleanly resolve to one from + one to (e.g., castling, en
// passant, or multi-ply jump). Used when state advances externally (another
// client registered the move) so we can still reflect it in the tape.
function inferSingleMove(
  prev: (string | null)[][],
  curr: (string | null)[][]
): { from: string; to: string } | null {
  let from: string | null = null;
  let to: string | null = null;
  let extras = 0;
  for (let r = 0; r < 8; r++) {
    for (let c = 0; c < 8; c++) {
      const a = prev[r]?.[c] ?? null;
      const b = curr[r]?.[c] ?? null;
      if (a === b) continue;
      const sq = rcToSq(r, c);
      if (a && !b) {
        if (from === null) from = sq;
        else extras++;
      } else if (!a && b) {
        if (to === null) to = sq;
        else extras++;
      } else if (a && b && a !== b) {
        if (to === null) to = sq;
        else extras++;
      }
    }
  }
  if (from && to && extras === 0) return { from, to };
  return null;
}

// (Detection lives on the server — see runAutoLoop in play.go. The client
// reflects detected plies via the FEN-diff inference path in applySnapshot.)

// ── Connection ─────────────────────────────────────────────────────────────

async function connect() {
  const cookieKey = window.location.pathname.split("/")[2];
  let raw = Cookies.get(cookieKey);
  // local-app-testing proxy names the cookie after the machine ID, not the path segment.
  if (!raw) {
    for (const val of Object.values(Cookies.get())) {
      try { if ((JSON.parse(val) as MachineCookie).apiKey) { raw = val; break; } } catch {}
    }
  }
  if (!raw) throw new Error("Viam machine cookie not found — open this app from the Viam portal.");
  const cookie: MachineCookie = JSON.parse(raw);
  const { apiKey, hostname, machineName } = cookie;

  setStatus("Connecting", "warn");
  const machineEl = document.getElementById("machine-name") as HTMLAnchorElement | null;
  if (machineEl) machineEl.textContent = machineName || hostname.split(".")[0] || "—";

  const robot = await createRobotClient({
    host: hostname,
    signalingAddress: "https://app.viam.com:443",
    credentials: { type: "api-key", payload: apiKey.key, authEntity: apiKey.id },
  });
  robotClient = robot;
  chessService = new GenericServiceClient(robot, CHESS_SERVICE_NAME);
  setStatus("in sync", "ok");

  void resolveMachineLink(machineEl);
}

async function resolveMachineLink(el: HTMLAnchorElement | null) {
  if (!el || !robotClient) return;
  try {
    const meta = await robotClient.getCloudMetadata();
    if (!meta.machineId || !meta.primaryOrgId) return;
    el.href = `https://app.viam.com/machine/${meta.machineId}/control?org=${meta.primaryOrgId}`;
  } catch (e) {
    console.warn("machine link: cloud metadata failed", e);
  }
}

async function doCommand(cmd: Record<string, unknown>): Promise<Record<string, JsonValue>> {
  if (!chessService) throw new Error("Not connected");
  const result = await chessService.doCommand(Struct.fromJson(cmd as JsonValue));
  const res = (result ?? {}) as Record<string, JsonValue>;
  // Every DoCommand success payload carries the current mode — keep ours fresh.
  // The mute window keeps a poll that was already in flight when an explicit
  // mode command landed from briefly reverting the fresher mode.
  if (typeof res.mode === "number" && Date.now() >= modeSnapshotMuteUntil) {
    updateMode(res.mode);
  }
  return res;
}

let modeSnapshotMuteUntil = 0;

// ── Mode machine mirror ────────────────────────────────────────────────────

function updateMode(mode: number) {
  if (modeKnown && mode === currentMode) return;
  const first = !modeKnown;
  const prev = currentMode;
  currentMode = mode;
  modeKnown = true;
  if (!first && mode !== prev) {
    pushEvent("mode", `${MODE_NAMES[prev] ?? prev} → ${MODE_NAMES[mode] ?? mode}`);
  }
  // IDLE/ERROR need resume targets we can only get from mode-status.
  if (mode === MODE_IDLE || mode === MODE_ERROR) void refreshModeStatus();
  renderModePanel();
}

let modeStatusInFlight = false;
async function refreshModeStatus() {
  if (modeStatusInFlight || !chessService) return;
  modeStatusInFlight = true;
  try {
    const res = await doCommand({ "mode-status": true });
    if (typeof res.idle_origin === "number") idleOrigin = res.idle_origin;
    if (typeof res.err_prev_mode === "number") errPrevMode = res.err_prev_mode;
    if (typeof res.game_over === "boolean") gameOver = res.game_over;
    renderModePanel();
  } catch (e) {
    console.warn("mode-status failed", e);
  } finally {
    modeStatusInFlight = false;
  }
}

async function setModeOnServer(mode: number): Promise<boolean> {
  try {
    // The command's own response applies the new mode (doCommand); then mute
    // snapshot-derived mode updates briefly — see doCommand.
    await doCommand({ mode });
    modeSnapshotMuteUntil = Date.now() + 2_000;
    renderModePanel();
    return true;
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    pushEvent("err", `mode → ${MODE_NAMES[mode] ?? mode}: ${msg}`);
    // The transition was rejected — our mirror may be stale; resync.
    void refreshState();
    return false;
  }
}

// Blocks gameplay commands (go/move/undo) in states where they'd fail or be
// destructive; optionally auto-starts a VS_HUMAN game from START (the boot
// state wipes any saved game every tick, so playing there would be silently
// lost). Returns a user-facing reason when blocked, null when clear to play.
async function ensurePlayable(autoStart = true): Promise<string | null> {
  if (currentMode === MODE_ERROR) {
    return "robot fault — resume or reset first (see banner)";
  }
  if (gameOver) {
    return "game over — reset the board to play again";
  }
  if (currentMode === MODE_START) {
    if (!autoStart) return "no active game";
    if (!(await setModeOnServer(MODE_VS_HUMAN))) return "couldn't start a game";
    companion.onGameStarted();
  }
  return null;
}

// ── Status pill ────────────────────────────────────────────────────────────

function setStatus(label: string, level: "ok" | "warn" | "err") {
  const pill = document.getElementById("status-pill");
  const labelEl = pill?.querySelector(".status-pill-label") as HTMLElement | null;
  if (!pill || !labelEl) return;
  pill.classList.remove("warn", "err");
  if (level === "warn") pill.classList.add("warn");
  if (level === "err") pill.classList.add("err");
  labelEl.textContent = label;
}

function updateStatusFromMismatches() {
  if (currentMode === MODE_ERROR) {
    setStatus("fault", "err");
    return;
  }
  if (needsFix) {
    setStatus("fix board", "warn");
    return;
  }
  const suffix = idleMode ? " · idle" : "";
  if (mismatches.length === 0) setStatus("in sync" + suffix, "ok");
  else setStatus(`${mismatches.length} diff${suffix}`, "warn");
}

// ── Mode control (top bar) + fault banner ──────────────────────────────────

interface ModeAction {
  label: string;
  accent?: boolean;
  danger?: boolean;
  run: () => void;
}

function modeChipState(): { label: string; cls: string } {
  if (!modeKnown) return { label: "—", cls: "" };
  switch (currentMode) {
    case MODE_START: return { label: "no game", cls: "" };
    case MODE_IDLE: return gameOver
      ? { label: "game over", cls: "attention" }
      : { label: "paused", cls: "attention" };
    case MODE_VS_HUMAN: return { label: "vs human", cls: "playing" };
    case MODE_VS_SELF: return { label: "self-play", cls: "playing" };
    case MODE_TEACHING: return { label: "teaching", cls: "playing" };
    case MODE_ERROR: return { label: "fault", cls: "fault" };
    default: return { label: `mode ${currentMode}`, cls: "" };
  }
}

function modeActions(): ModeAction[] {
  if (!modeKnown) return [];
  const to = (mode: number) => () => void setModeOnServer(mode);
  switch (currentMode) {
    case MODE_START:
      return [
        { label: "▶ Play vs Garry", accent: true, run: () => { void setModeOnServer(MODE_VS_HUMAN).then((ok) => { if (ok) companion.onGameStarted(); }); } },
        { label: "Self-play", run: () => { void setModeOnServer(MODE_VS_SELF).then((ok) => { if (ok) companion.onGameStarted(); }); } },
      ];
    case MODE_VS_HUMAN:
      return [
        { label: "⏸ Pause", run: to(MODE_IDLE) },
        { label: "Teach", run: to(MODE_TEACHING) },
      ];
    case MODE_VS_SELF:
      return [{ label: "⏸ Pause", run: to(MODE_IDLE) }];
    case MODE_TEACHING:
      return [
        { label: "▶ Back to game", accent: true, run: to(MODE_VS_HUMAN) },
        { label: "⏸ Pause", run: to(MODE_IDLE) },
      ];
    case MODE_IDLE:
      if (gameOver) {
        return [
          { label: "Reset board", danger: true, run: () => void cmdMaintenance("reset") },
          { label: "New game", run: to(MODE_START) },
        ];
      }
      return [
        // idleOrigin is 0 until mode-status answers; VS_HUMAN is the only
        // origin reachable from this UI anyway, so it's a safe fallback.
        { label: "▶ Resume", accent: true, run: to(idleOrigin || MODE_VS_HUMAN) },
        { label: "End game", danger: true, run: to(MODE_START) },
      ];
    case MODE_ERROR:
      return [{ label: "Reset state", danger: true, run: to(MODE_START) }];
    default:
      return [];
  }
}

function renderModePanel() {
  const chipEl = document.getElementById("mode-chip");
  const actionsEl = document.getElementById("mode-actions");
  if (chipEl) {
    const { label, cls } = modeChipState();
    chipEl.textContent = label;
    chipEl.className = "mode-chip mono" + (cls ? " " + cls : "");
  }
  if (actionsEl) {
    actionsEl.innerHTML = "";
    for (const action of modeActions()) {
      const btn = document.createElement("button");
      btn.className = "btn btn-sm" + (action.accent ? " btn-accent" : "") + (action.danger ? " danger" : "");
      btn.textContent = action.label;
      btn.disabled = busy;
      btn.addEventListener("click", action.run);
      actionsEl.appendChild(btn);
    }
  }

  // Fault banner
  const banner = document.getElementById("error-banner");
  if (banner) banner.classList.toggle("hidden", currentMode !== MODE_ERROR);
  const resumeBtn = document.getElementById("btn-error-resume") as HTMLButtonElement | null;
  if (resumeBtn) {
    // Resume target comes from mode-status; hide until we know it. A fault in
    // START has nowhere to resume to (errPrevMode 0) — reset is the only path.
    resumeBtn.classList.toggle("hidden", errPrevMode === 0);
  }

  applyCommandAvailability();
  updateStatusFromMismatches();
}

// Gameplay commands are unavailable in ERROR and after game over; the physical
// board reset is rejected by the server in ERROR (arm may be unsafe).
function applyCommandAvailability() {
  const gameplayBlocked = currentMode === MODE_ERROR || gameOver;
  const gameplayTitle =
    currentMode === MODE_ERROR ? "robot fault — recover first" :
    gameOver ? "game over — reset the board to play again" : "";
  for (const id of ["btn-go", "btn-move", "btn-undo"]) {
    const btn = document.getElementById(id) as HTMLButtonElement | null;
    if (!btn) continue;
    btn.disabled = busy || gameplayBlocked;
    btn.title = gameplayTitle || (id === "btn-undo" ? "Undo last ply" : "");
  }
  const resetBtn = document.getElementById("btn-reset") as HTMLButtonElement | null;
  if (resetBtn) {
    resetBtn.disabled = busy || currentMode === MODE_ERROR;
    resetBtn.title = currentMode === MODE_ERROR ? "physical reset is disabled while faulted — use Reset state" : "";
  }
}

// ── Eval bar ───────────────────────────────────────────────────────────────

// Converts centipawns to white's fill percentage (0–100) using the same
// sigmoid formula lichess uses so the bar feels natural and never hard-clips.
function cpToWhitePct(cp: number, mate: number): number {
  if (mate > 0) return 100;
  if (mate < 0) return 0;
  return Math.round(50 + 50 * (2 / (1 + Math.exp(-0.004 * cp)) - 1));
}

function renderEvalBar() {
  const fill = document.getElementById("eval-bar-fill");
  const label = document.getElementById("eval-bar-label");
  if (!fill || !label) return;

  const pct = cpToWhitePct(currentScoreCP, currentScoreMate);
  fill.style.height = `${pct}%`;

  if (currentScoreMate > 0) {
    label.textContent = `M${currentScoreMate}`;
  } else if (currentScoreMate < 0) {
    label.textContent = `M${-currentScoreMate}`;
  } else {
    const abs = Math.abs(currentScoreCP / 100).toFixed(1);
    label.textContent = currentScoreCP > 0 ? `+${abs}` : currentScoreCP < 0 ? `-${abs}` : "0.0";
  }
}

// ── Board rendering ────────────────────────────────────────────────────────

function renderBoard() {
  const boardEl = document.getElementById("board");
  if (!boardEl) return;
  boardEl.innerHTML = "";

  const mmBySq = new Map<string, Mismatch>();
  mismatches.forEach((m) => mmBySq.set(m.sq, m));

  for (let r = 0; r < 8; r++) {
    for (let c = 0; c < 8; c++) {
      const sq = rcToSq(r, c);
      const isLight = (r + c) % 2 === 0;
      const piece = currentBoard[r]?.[c] ?? null;
      const mm = mmBySq.get(sq);

      const cell = document.createElement("div");
      cell.className = "chess-square " + (isLight ? "light" : "dark");
      cell.dataset.sq = sq;
      if (mm) cell.classList.add("mismatch-" + mm.kind);

      // last-move highlight
      if (lastMove && (lastMove.from === sq || lastMove.to === sq)) {
        const h = document.createElement("div");
        h.className = "last-highlight";
        cell.appendChild(h);
      }

      // selection ring
      if (selectedSq === sq) {
        const s = document.createElement("div");
        s.className = "selected-ring";
        cell.appendChild(s);
      }

      // mismatch tint + dot
      if (mm) {
        const tint = document.createElement("div");
        tint.className = "mismatch-tint";
        cell.appendChild(tint);
        const dot = document.createElement("div");
        dot.className = "mismatch-dot";
        cell.appendChild(dot);
      }

      // piece
      if (piece) {
        const url = pieceUrl(piece);
        if (url) {
          const img = document.createElement("img");
          img.className = "piece";
          img.src = url;
          img.alt = piece;
          img.draggable = true;
          img.dataset.sq = sq;
          img.addEventListener("dragstart", (e) => {
            if (busy) {
              e.preventDefault();
              return;
            }
            e.dataTransfer?.setData("text/plain", sq);
            selectedSq = sq;
          });
          cell.appendChild(img);
        }
      }

      // camera detection dot
      if (cameraBoard) {
        const reading = cameraBoard[sq];
        if (reading === "1" || reading === "2") {
          const dot = document.createElement("div");
          dot.className = "cam-dot " + (reading === "1" ? "white" : "black");
          cell.appendChild(dot);
        }
      }

      // coordinates
      const showFile = r === 7;
      const showRank = c === 0;
      if (showFile) {
        const f = document.createElement("span");
        f.className = "coord file";
        f.textContent = String.fromCharCode(97 + c);
        cell.appendChild(f);
      }
      if (showRank) {
        const ra = document.createElement("span");
        ra.className = "coord rank";
        ra.textContent = String(8 - r);
        cell.appendChild(ra);
      }

      // interactions
      cell.addEventListener("click", () => onSquareClick(sq));
      cell.addEventListener("dragover", (e) => {
        e.preventDefault();
        cell.classList.add("drag-over");
        if (!cell.querySelector(".drag-over-ring")) {
          const d = document.createElement("div");
          d.className = "drag-over-ring";
          cell.appendChild(d);
        }
      });
      cell.addEventListener("dragleave", () => {
        cell.classList.remove("drag-over");
        cell.querySelector(".drag-over-ring")?.remove();
      });
      cell.addEventListener("drop", (e) => {
        e.preventDefault();
        cell.classList.remove("drag-over");
        cell.querySelector(".drag-over-ring")?.remove();
        const from = e.dataTransfer?.getData("text/plain");
        if (from && from !== sq) void submitMove(from, sq);
        selectedSq = null;
      });

      boardEl.appendChild(cell);
    }
  }
}

function onSquareClick(sq: string) {
  if (busy) return;
  if (!selectedSq) {
    const [r, c] = [8 - parseInt(sq[1], 10), sq.charCodeAt(0) - 97];
    if (currentBoard[r]?.[c]) {
      selectedSq = sq;
      renderBoard();
    }
    return;
  }
  if (selectedSq === sq) {
    selectedSq = null;
    renderBoard();
    return;
  }
  const from = selectedSq;
  selectedSq = null;
  void submitMove(from, sq);
}

// ── Turn + last move + status ──────────────────────────────────────────────

function renderTopStatus() {
  const turnEl = document.getElementById("turn-indicator");
  if (turnEl) turnEl.textContent = currentTurn === "w" ? "White to move" : "Black to move";

  const lastMoveEl = document.getElementById("last-move");
  const lastMoveRule = document.getElementById("last-move-rule");
  if (lastMove && lastMoveEl && lastMoveRule) {
    lastMoveEl.classList.remove("hidden");
    lastMoveRule.classList.remove("hidden");
    (lastMoveEl.querySelector(".last-from") as HTMLElement).textContent = lastMove.from;
    (lastMoveEl.querySelector(".last-to") as HTMLElement).textContent = lastMove.to;
  } else if (lastMoveEl && lastMoveRule) {
    lastMoveEl.classList.add("hidden");
    lastMoveRule.classList.add("hidden");
  }
}

// ── Material + captured ────────────────────────────────────────────────────

function renderMaterial() {
  // captured.b (white lost) ↔ white_graveyard
  // captured.w (black lost) ↔ black_graveyard
  const wLost = whiteGraveyard;
  const bLost = blackGraveyard;
  const wPts = sumValue(wLost);
  const bPts = sumValue(bLost);
  const balance = wPts - bPts;

  const wEl = document.getElementById("scale-wpts");
  const bEl = document.getElementById("scale-bpts");
  if (wEl) wEl.textContent = String(wPts);
  if (bEl) bEl.textContent = String(bPts);

  const beam = document.getElementById("scale-beam");
  if (beam) {
    const tilt = Math.max(-12, Math.min(12, balance * 1.4));
    beam.style.transform = `translateX(-50%) rotate(${-tilt}deg)`;
  }

  const balLabel = document.getElementById("balance-label");
  if (balLabel) {
    if (balance === 0) {
      balLabel.textContent = "even";
      balLabel.classList.remove("nonzero");
    } else if (balance > 0) {
      balLabel.textContent = `white +${balance}`;
      balLabel.classList.add("nonzero");
    } else {
      balLabel.textContent = `black +${-balance}`;
      balLabel.classList.add("nonzero");
    }
  }

  renderCaptured("white-lost", wLost);
  renderCaptured("black-lost", bLost);
}

function renderCaptured(id: string, pieces: string[]) {
  const el = document.getElementById(id);
  if (!el) return;
  el.innerHTML = "";
  if (pieces.length === 0) {
    const empty = document.createElement("span");
    empty.className = "empty";
    empty.textContent = "— none —";
    el.appendChild(empty);
    return;
  }
  for (const p of pieces) {
    const url = pieceUrl(p);
    if (!url) continue;
    const img = document.createElement("img");
    img.className = "captured-piece";
    img.src = url;
    img.alt = p;
    el.appendChild(img);
  }
}

// ── Tape ───────────────────────────────────────────────────────────────────

function renderTape() {
  const tapeEl = document.getElementById("tape");
  const plyEl = document.getElementById("tape-ply");
  if (!tapeEl) return;
  if (plyEl) plyEl.textContent = `${plyCount} ply`;

  tapeEl.innerHTML = "";

  if (tapeItems.length === 0) {
    const e = document.createElement("div");
    e.className = "empty";
    e.textContent = "awaiting first move";
    tapeEl.appendChild(e);
    return;
  }

  let lastMoveIdx = -1;
  for (let k = tapeItems.length - 1; k >= 0; k--) {
    const it = tapeItems[k];
    if (it.kind === "move") { lastMoveIdx = it.i; break; }
  }

  // Newest first — iterate in reverse.
  for (let k = tapeItems.length - 1; k >= 0; k--) {
    const it = tapeItems[k];
    if (it.kind === "evt" && !showTapeLogs) continue;
    if (it.kind === "evt") {
      const row = document.createElement("div");
      row.className = "tape-evt";
      if (it.type === "err") row.classList.add("err");
      const tag = document.createElement("span");
      tag.className = "tape-evt-tag " + it.type;
      tag.textContent = it.type;
      const label = document.createElement("span");
      label.className = "tape-evt-label";
      label.textContent = it.label;
      row.appendChild(tag);
      row.appendChild(label);
      tapeEl.appendChild(row);
      continue;
    }
    const row = document.createElement("div");
    row.className = "tape-row";
    if (it.i === lastMoveIdx) row.classList.add("last");
    const moveNum = Math.floor(it.i / 2) + 1;
    const isWhite = it.color === "w";
    const num = document.createElement("span");
    num.className = "tape-num";
    num.textContent = isWhite ? `${moveNum}.` : "";
    const sw = document.createElement("span");
    sw.className = "tape-swatch " + (isWhite ? "white" : "black");
    const san = document.createElement("span");
    san.className = "tape-san";
    san.textContent = it.san;
    const coord = document.createElement("span");
    coord.className = "tape-coord";
    coord.textContent = `${it.from}→${it.to}`;
    row.appendChild(num);
    row.appendChild(sw);
    row.appendChild(san);
    row.appendChild(coord);
    tapeEl.appendChild(row);
  }
}

function pushEvent(type: EvtType, label: string) {
  tapeItems.push({ kind: "evt", type, label });
  renderTape();
  persistState();
}

// ── Inline error popovers ──────────────────────────────────────────────────

const inlineErrorTimers: Record<string, ReturnType<typeof setTimeout>> = {};

function showInlineError(which: "go" | "move", msg: string) {
  const popId = which === "go" ? "go-error" : "move-error";
  const inputIds = which === "go" ? ["go-n"] : ["move-from", "move-to"];
  const pop = document.getElementById(popId);
  if (!pop) return;
  pop.classList.remove("hidden");
  (pop.querySelector(".inline-error-msg") as HTMLElement).textContent = msg;
  inputIds.forEach((id) => document.getElementById(id)?.classList.add("error"));
  if (inlineErrorTimers[which]) clearTimeout(inlineErrorTimers[which]);
  inlineErrorTimers[which] = setTimeout(() => dismissInlineError(which), 10_000);
}
function dismissInlineError(which: "go" | "move") {
  const popId = which === "go" ? "go-error" : "move-error";
  const inputIds = which === "go" ? ["go-n"] : ["move-from", "move-to"];
  if (inlineErrorTimers[which]) { clearTimeout(inlineErrorTimers[which]); delete inlineErrorTimers[which]; }
  document.getElementById(popId)?.classList.add("hidden");
  inputIds.forEach((id) => document.getElementById(id)?.classList.remove("error"));
}

// ── State application ──────────────────────────────────────────────────────

const STARTING_PLACEMENT = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR";

function applySnapshot(res: Record<string, JsonValue>) {
  let inferredExternal: { from: string; to: string } | null = null;
  let observedReset = false;

  // Mode-machine fields. (mode itself is already applied by doCommand.)
  if (typeof res.game_over === "boolean" && res.game_over !== gameOver) {
    gameOver = res.game_over;
    renderModePanel();
  }
  if (typeof res.needs_fix === "boolean") needsFix = res.needs_fix;

  // VS_HUMAN drag-move: hand board authority back once the server has
  // registered our ply (FEN caught up) or told us the board needs fixing
  // (registration failed — show the real server state + mismatches).
  if (!serverAuthoritative && pendingMovePly !== null && typeof res.fen === "string") {
    if (plyCountFromFEN(res.fen) >= pendingMovePly || needsFix) {
      serverAuthoritative = true;
      pendingMovePly = null;
    }
  }

  let fenChanged = false;
  if (serverAuthoritative) {
    if (typeof res.fen === "string") {
      const prevFen = currentFen;
      currentFen = res.fen;
      const { board, turn } = parseFENPlacement(currentFen);
      // Diff against the board as last rendered (not as last received): after
      // an optimistic drag move in VS_HUMAN, currentBoard already holds the
      // human ply, so the robot's reply still resolves to a single-move diff.
      const prevBoard = prevFen ? currentBoard.map((row) => [...row]) : null;
      currentBoard = board;
      currentTurn = turn;
      if (prevFen && prevFen !== currentFen) {
        fenChanged = true;
        console.debug("[fen]", prevFen, "→", currentFen);
        const prevPlacement = prevFen.split(" ")[0];
        const currPlacement = currentFen.split(" ")[0];
        if (prevPlacement !== STARTING_PLACEMENT && currPlacement === STARTING_PLACEMENT) {
          // FEN jumped back to starting position — someone called wipe/reset.
          // Clear our tape so we don't display stale history alongside a fresh game.
          observedReset = true;
        } else if (suppressFenInferOnce) {
          suppressFenInferOnce = false;
        } else if (prevBoard) {
          const inferred = inferSingleMove(prevBoard, currentBoard);
          if (inferred && (lastMove?.from !== inferred.from || lastMove?.to !== inferred.to)) {
            // FEN advanced by one move and we didn't push it — another client
            // (or a path we don't own) registered it. Reflect it in the tape.
            inferredExternal = inferred;
          }
        }
      }
    }
    const prevBlackGraveyardLen = blackGraveyard.length;
    if (Array.isArray(res.white_graveyard)) whiteGraveyard = res.white_graveyard as string[];
    if (Array.isArray(res.black_graveyard)) blackGraveyard = res.black_graveyard as string[];
    if (initialLoaded && prevBlackGraveyardLen === 0 && blackGraveyard.length > 0) {
      companion.onFirstCapture();
    }
  }
  cameraBoard =
    res.camera_board && typeof res.camera_board === "object"
      ? (res.camera_board as Record<string, string>)
      : null;
  mismatches = diffCamera(currentBoard, cameraBoard);

  if (typeof res.score_cp === "number") currentScoreCP = res.score_cp as number;
  if (typeof res.score_mate === "number") currentScoreMate = res.score_mate as number;

  renderBoard();
  renderMaterial();
  renderTopStatus();
  renderEvalBar();
  updateStatusFromMismatches();

  // outcome from board-snapshot is now in GameEventsResult format ("white_won", "black_won",
  // "draw", "in_progress"). Normalize to companion's hyphen format.
  const rawOutcome = typeof res.outcome === "string" ? res.outcome : "";
  const snapshotOutcome: GameOutcome =
    rawOutcome === "white_won" ? "white-won" :
    rawOutcome === "black_won" ? "black-won" :
    rawOutcome === "draw" ? "draw" : "";
  const snapshotInCheck = res.in_check === true;
  // FEN-derived ply count: client tape plyCount stays 0 when white+black both
  // arrive between polls (inferSingleMove returns null for multi-ply jumps).
  const fenPly = currentFen ? plyCountFromFEN(currentFen) : plyCount;

  if (observedReset) {
    resetTape();
    lastMove = null;
    currentScoreCP = 0;
    currentScoreMate = 0;
    pushEvent("reset", "observed reset from another client");
    renderTape();
    renderTopStatus();
    if (idleMode) setIdle(false);
  } else if (inferredExternal) {
    pushMoveToTape(inferredExternal.from, inferredExternal.to, `${inferredExternal.from}${inferredExternal.to}`);
    pushEvent("go", "inferred from fen diff");
  }
  // Any externally-advancing game (VS_SELF, another client, the board loop
  // replying) should keep the UI awake even without local user activity.
  if (fenChanged && idleMode) setIdle(false);

  if (!initialLoaded) {
    companion.onInit(fenPly, gameActive(), mismatches.length, snapshotOutcome, snapshotInCheck, needsFix);
    initialLoaded = true;
    document.getElementById("board-loading")?.classList.add("hidden");
  } else if (observedReset) {
    companion.onReset();
  } else {
    companion.onSnapshot(fenPly, gameActive(), mismatches.length, snapshotOutcome, snapshotInCheck, needsFix);
  }
}

function pushMoveToTape(from: string, to: string, san: string) {
  const i = plyCount++;
  const color: "w" | "b" = i % 2 === 0 ? "w" : "b";
  tapeItems.push({ kind: "move", i, from, to, san, color });
  lastMove = { from, to, san };
  renderTape();
  renderTopStatus();
  persistState();
  companion.onMove(plyCount);
}

function resetTape() {
  tapeItems = [];
  plyCount = 0;
  persistState();
}

// ── Persistence ────────────────────────────────────────────────────────────
// Client-only state (tape, ply count, last-move readout) survives a page
// reload. Graveyards/board/auto come from the server on reconnect.

const STORAGE_KEY = "garry.chess.state.v2";
const mockMode = new URLSearchParams(window.location.search).has("mock");

function persistState() {
  if (mockMode) return;
  try {
    localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({ tapeItems, plyCount, lastMove })
    );
  } catch (e) {
    console.warn("[persist] save failed", e);
  }
}

function loadPersistedState() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return;
    const data = JSON.parse(raw);
    if (Array.isArray(data.tapeItems)) tapeItems = data.tapeItems;
    if (typeof data.plyCount === "number") plyCount = data.plyCount;
    if (data.lastMove && typeof data.lastMove.from === "string") lastMove = data.lastMove;
  } catch (e) {
    console.warn("[persist] load failed", e);
  }
}

// Seed fake tape + graveyards + a mid-game FEN so the UI can be inspected
// visually without a running game. Triggered by `?mock` in the URL.
function seedMockState() {
  tapeItems = [];
  plyCount = 0;
  const pushMove = (from: string, to: string) => {
    const i = plyCount++;
    tapeItems.push({ kind: "move", i, from, to, san: `${from}${to}`, color: i % 2 === 0 ? "w" : "b" });
  };
  tapeItems.push({ kind: "evt", type: "wipe", label: "state wiped" });
  tapeItems.push({ kind: "evt", type: "go", label: "auto: on" });
  pushMove("e2", "e4");
  tapeItems.push({ kind: "evt", type: "go", label: "auto · replying" });
  pushMove("e7", "e5");
  tapeItems.push({ kind: "evt", type: "go", label: "auto · e7e5" });
  pushMove("g1", "f3");
  pushMove("b8", "c6");
  tapeItems.push({ kind: "evt", type: "go", label: "inferred from fen diff" });
  pushMove("f1", "b5");
  pushMove("a7", "a6");
  tapeItems.push({
    kind: "evt",
    type: "err",
    label: "auto go: [unknown] bad number of differences (1) : [h4]",
  });
  pushMove("b5", "a4");
  pushMove("g8", "f6");
  tapeItems.push({ kind: "evt", type: "go", label: "auto · g8f6" });
  const last = tapeItems[tapeItems.length - 2];
  if (last && last.kind === "move") lastMove = { from: last.from, to: last.to, san: last.san };

  whiteGraveyard = ["P", "P", "N"];
  blackGraveyard = ["p", "b"];
  currentScoreCP = 52;
  currentScoreMate = 0;
  currentFen = "r1bqkb1r/1ppp1ppp/p1n2n2/4p3/B3P3/5N2/PPPP1PPP/RNBQK2R w KQkq - 3 5";
  const parsed = parseFENPlacement(currentFen);
  currentBoard = parsed.board;
  currentTurn = parsed.turn;
}

// ── Refresh ────────────────────────────────────────────────────────────────

async function refreshState() {
  if (refreshInFlight || !chessService) return;
  refreshInFlight = true;
  try {
    const res = await doCommand({ "board-snapshot": true });
    applySnapshot(res);
  } catch (e) {
    console.error("refresh failed", e);
  } finally {
    refreshInFlight = false;
  }
}

function startAutoRefresh() {
  if (autoRefreshTimer) clearInterval(autoRefreshTimer);
  autoRefreshTimer = setInterval(refreshState, refreshPollMs);
}

// ── Idle mode ──────────────────────────────────────────────────────────────
// After IDLE_THRESHOLD_MS without user activity, slow snapshot polling to
// IDLE_POLL_MS. Wake on user input, an observed FEN change, or a reset event.

function setIdle(idle: boolean) {
  if (idleMode === idle) return;
  idleMode = idle;
  refreshPollMs = idle ? IDLE_POLL_MS : ACTIVE_REFRESH_MS;
  startAutoRefresh();
  updateStatusFromMismatches();
  // Drop the WebRTC stream while idle — it's pure bandwidth otherwise — and
  // resume it on wake if the panel is still expanded.
  const panel = document.getElementById("cam-panel");
  if (panel && !panel.classList.contains("collapsed")) {
    if (idle) {
      void detachCamera();
      setCamStatus("paused");
    } else {
      void attachCamera();
    }
  }
  pushEvent("go", `idle: ${idle ? "sleeping" : "waking"}`);
}

function recordActivity() {
  lastActivityAt = Date.now();
  if (idleMode) setIdle(false);
}

function startIdleWatch() {
  ["mousemove", "keydown", "touchstart", "click"].forEach((evt) => {
    document.addEventListener(evt, recordActivity, { passive: true });
  });
  setInterval(() => {
    // Never idle during self-play — the game advances with nobody at the kiosk.
    if (!idleMode && currentMode !== MODE_VS_SELF && Date.now() - lastActivityAt > IDLE_THRESHOLD_MS) {
      setIdle(true);
    }
  }, 10_000);
}

// ── Commands ───────────────────────────────────────────────────────────────

function setBusy(next: boolean) {
  busy = next;
  document.querySelectorAll<HTMLButtonElement>("button").forEach((b) => (b.disabled = next));
  // Re-applies the mode-based gating that the blanket re-enable above undoes.
  if (!next) applyCommandAvailability();
}

async function withBusy(fn: () => Promise<void>) {
  setBusy(true);
  if (autoRefreshTimer) clearInterval(autoRefreshTimer);
  try {
    await fn();
    await refreshState();
  } finally {
    setBusy(false);
    startAutoRefresh();
  }
}

function popLastMoveFromTape() {
  for (let i = tapeItems.length - 1; i >= 0; i--) {
    if (tapeItems[i].kind === "move") {
      tapeItems.splice(i, 1);
      plyCount = Math.max(0, plyCount - 1);
      break;
    }
  }
  lastMove = null;
}

async function cmdUndo() {
  const n = 1;
  const blocked = await ensurePlayable(false);
  if (blocked) {
    pushEvent("err", `undo: ${blocked}`);
    return;
  }
  serverAuthoritative = true;
  try {
    await withBusy(async () => {
      suppressFenInferOnce = true;
      await doCommand({ undo: n });
      popLastMoveFromTape();
      pushEvent("undo", `undo ×${n}`);
    });
  } catch (e) {
    suppressFenInferOnce = false;
    const msg = e instanceof Error ? e.message : String(e);
    pushEvent("err", `undo ${n}: ${msg}`);
  }
}

async function cmdGo() {
  dismissInlineError("go");
  const n = parseInt((document.getElementById("go-n") as HTMLInputElement).value, 10) || 1;
  const blocked = await ensurePlayable();
  if (blocked) {
    showInlineError("go", blocked);
    return;
  }
  serverAuthoritative = true;
  try {
    await withBusy(async () => {
      // `go`'s first iteration runs checkPositionForMoves itself, so any
      // unregistered human ply gets recorded server-side; the FEN-diff
      // inference in applySnapshot picks it up into the tape.
      const res = await doCommand({ go: n });
      const move = typeof res.move === "string" ? res.move : "";
      const m = move.match(/^([a-h][1-8])[-\s]?([a-h][1-8])/);
      if (m) pushMoveToTape(m[1], m[2], move);
      pushEvent("go", `robot ×${n}${move ? ` · ${move}` : ""}`);
    });
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    showInlineError("go", msg);
    pushEvent("err", `go ${n}: ${msg}`);
  }
}

function applyLocalMove(from: string, to: string): boolean {
  const fr = 8 - parseInt(from[1], 10);
  const fc = from.charCodeAt(0) - 97;
  const tr = 8 - parseInt(to[1], 10);
  const tc = to.charCodeAt(0) - 97;
  const piece = currentBoard[fr]?.[fc];
  if (!piece) return false;
  const captured = currentBoard[tr]?.[tc] ?? null;
  currentBoard[tr][tc] = piece;
  currentBoard[fr][fc] = null;
  if (captured) {
    if (captured === captured.toUpperCase()) whiteGraveyard = [...whiteGraveyard, captured];
    else blackGraveyard = [...blackGraveyard, captured];
  }
  currentTurn = currentTurn === "w" ? "b" : "w";
  lastMove = { from, to, san: `${from}${to}` };
  mismatches = diffCamera(currentBoard, cameraBoard);
  return true;
}

async function submitMove(from: string, to: string) {
  dismissInlineError("move");
  if (!/^[a-h][1-8]$/.test(from) || !/^[a-h][1-8]$/.test(to)) {
    showInlineError("move", `invalid square: ${from}→${to}`);
    return;
  }
  // Gate on mode first (may auto-start a VS_HUMAN game from START).
  const blocked = await ensurePlayable();
  if (blocked) {
    showInlineError("move", blocked);
    return;
  }
  // Snapshot for revert on failure.
  const prev = {
    board: currentBoard.map((row) => [...row]),
    white: whiteGraveyard,
    black: blackGraveyard,
    turn: currentTurn,
    lastMove,
    mismatches,
  };
  if (!applyLocalMove(from, to)) {
    showInlineError("move", `no piece on ${from}`);
    return;
  }
  renderBoard();
  renderMaterial();
  renderTopStatus();
  updateStatusFromMismatches();

  // Hold the UI as authoritative while the server's FEN is behind our
  // optimistic move; the hand-back differs per mode (see below).
  serverAuthoritative = false;

  // In VS_HUMAN the board loop watches the camera, registers any human ply,
  // and replies as black on its own — sending {go} too would race it and can
  // make the engine play *white's* move. So a UI move is just an arm
  // actuation, exactly like moving the piece by hand; the loop does the rest
  // and applySnapshot hands authority back once the FEN catches up.
  const loopDriven = currentMode === MODE_VS_HUMAN;

  try {
    await withBusy(async () => {
      // 1. Arm physically moves the piece (no server game-state change).
      await doCommand({ move: { from, to, n: 1 } });
      // White piece is now on the target square — dismiss companion welcome.
      companion.onMove(plyCount + 1);

      if (loopDriven) {
        pendingMovePly = (currentFen ? plyCountFromFEN(currentFen) : plyCount) + 1;
        pushMoveToTape(from, to, `${from}${to}`);
        pushEvent("go", "arm moved — Garry will reply");
        return;
      }

      // Manual modes (paused game / teaching): the loop is passive, so drive
      // the game ourselves. `go 1` sanity-checks the camera, registers the
      // human ply on the server, then picks and plays the engine's response.
      const goRes = await doCommand({ go: 1 });
      serverAuthoritative = true;
      pushMoveToTape(from, to, `${from}${to}`);
      const mv = typeof goRes.move === "string" ? goRes.move : "";
      const parsed = mv.match(/^([a-h][1-8])[-\s]?([a-h][1-8])/);
      if (parsed) pushMoveToTape(parsed[1], parsed[2], mv);
    });
  } catch (e) {
    currentBoard = prev.board;
    whiteGraveyard = prev.white;
    blackGraveyard = prev.black;
    currentTurn = prev.turn;
    lastMove = prev.lastMove;
    mismatches = prev.mismatches;
    serverAuthoritative = true;
    pendingMovePly = null;
    renderBoard();
    renderMaterial();
    renderTopStatus();
    updateStatusFromMismatches();
    const msg = e instanceof Error ? e.message : String(e);
    showInlineError("move", msg);
    pushEvent("err", `direct ${from}→${to}: ${msg}`);
  }
}

async function cmdDirectMoveFromInputs() {
  const from = (document.getElementById("move-from") as HTMLInputElement).value.trim().toLowerCase();
  const to = (document.getElementById("move-to") as HTMLInputElement).value.trim().toLowerCase();
  if (!from || !to) {
    showInlineError("move", "enter both squares");
    return;
  }
  await submitMove(from, to);
  (document.getElementById("move-from") as HTMLInputElement).value = "";
  (document.getElementById("move-to") as HTMLInputElement).value = "";
}

async function cmdSetDifficulty(elo: number) {
  try {
    const res = await doCommand({ difficulty: elo });
    const applied = typeof res.difficulty === "number" ? res.difficulty as number : elo;
    if (applied !== elo) {
      pushEvent("go", `difficulty: ELO ${elo} → clamped to ${applied}`);
    } else {
      pushEvent("go", `difficulty: ELO ${elo}`);
    }
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    pushEvent("err", `difficulty: ${msg}`);
  }
}

async function cmdMaintenance(id: "refresh" | "snapshot" | "cache" | "wipe" | "reset", skipConfirm = false) {
  // The server rejects the physical reset in ERROR (arm may be unsafe);
  // surface that before the arm-confirm dialog rather than as a failed call.
  if (id === "reset" && currentMode === MODE_ERROR) {
    pushEvent("err", "reset: physical reset is disabled while faulted — use Reset state in the banner");
    return;
  }
  if (id === "reset" && !skipConfirm && !confirm("Physically reset the board?")) return;
  if (id === "wipe" && !skipConfirm && !confirm("Wipe game state?")) return;
  try {
    await withBusy(async () => {
      if (id === "refresh") {
        // handled by the post-action refresh
      } else if (id === "snapshot") {
        // snapshot is implicit in refresh; just log
      } else if (id === "cache") {
        await doCommand({ ClearCache: true });
      } else if (id === "wipe") {
        await doCommand({ wipe: true });
        resetTape();
        lastMove = null;
        serverAuthoritative = true;
      } else if (id === "reset") {
        await doCommand({ reset: true });
        resetTape();
        lastMove = null;
        serverAuthoritative = true;
      }
      pushEvent(id, labelFor(id));
    });
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    pushEvent("err", `${id}: ${msg}`);
  }
}

function labelFor(id: EvtType): string {
  switch (id) {
    case "refresh": return "state refreshed";
    case "snapshot": return "snapshot captured";
    case "cache": return "square cache cleared";
    case "wipe": return "state wiped";
    case "reset": return "board reset";
    default: return id;
  }
}

// ── Camera feed ────────────────────────────────────────────────────────────

function setCamStatus(text: string) {
  const el = document.getElementById("cam-status");
  if (el) el.textContent = text;
}

async function attachCamera() {
  if (camStream || camAttachInFlight) return;
  if (!robotClient) {
    setCamStatus("offline");
    return;
  }
  camAttachInFlight = true;
  setCamStatus("connecting…");
  try {
    if (!camStreamClient) camStreamClient = new StreamClient(robotClient);
    const stream = await camStreamClient.getStream(CAMERA_NAME);
    const video = document.getElementById("cam-video") as HTMLVideoElement | null;
    if (video) video.srcObject = stream;
    camStream = stream;
    setCamStatus("");
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    console.warn("camera attach failed", e);
    setCamStatus("error");
    pushEvent("err", `camera: ${msg}`);
  } finally {
    camAttachInFlight = false;
  }
}

async function detachCamera() {
  const video = document.getElementById("cam-video") as HTMLVideoElement | null;
  if (video) video.srcObject = null;
  camStream?.getTracks().forEach((t) => t.stop());
  camStream = null;
  if (camStreamClient) {
    try { await camStreamClient.remove(CAMERA_NAME); } catch (e) { console.debug("cam remove", e); }
  }
  setCamStatus("");
}

function toggleCamera() {
  const panel = document.getElementById("cam-panel");
  const toggle = document.getElementById("cam-toggle");
  if (!panel || !toggle) return;
  const willExpand = panel.classList.contains("collapsed");
  panel.classList.toggle("collapsed", !willExpand);
  toggle.setAttribute("aria-expanded", String(willExpand));
  if (willExpand) void attachCamera();
  else void detachCamera();
}

// ── Wire events ────────────────────────────────────────────────────────────

document.getElementById("btn-go")!.addEventListener("click", () => void cmdGo());
document.getElementById("tape-logs-toggle")!.addEventListener("click", () => {
  showTapeLogs = !showTapeLogs;
  document.getElementById("tape-logs-toggle")!.classList.toggle("active", showTapeLogs);
  renderTape();
});
document.getElementById("btn-undo")!.addEventListener("click", () => void cmdUndo());
document.getElementById("btn-move")!.addEventListener("click", () => void cmdDirectMoveFromInputs());
document.getElementById("btn-refresh")!.addEventListener("click", () => void cmdMaintenance("refresh"));
document.getElementById("btn-snapshot")!.addEventListener("click", () => void cmdMaintenance("snapshot"));
document.getElementById("btn-cache")!.addEventListener("click", () => void cmdMaintenance("cache"));
document.getElementById("btn-wipe")!.addEventListener("click", () => void cmdMaintenance("wipe"));
document.getElementById("btn-reset")!.addEventListener("click", () => void cmdMaintenance("reset"));
document.getElementById("move-to")!.addEventListener("keydown", (e) => {
  if ((e as KeyboardEvent).key === "Enter") void cmdDirectMoveFromInputs();
});
document.getElementById("go-n")!.addEventListener("keydown", (e) => {
  if ((e as KeyboardEvent).key === "Enter") void cmdGo();
});
document.getElementById("btn-error-resume")!.addEventListener("click", () => {
  if (errPrevMode > 0) void setModeOnServer(errPrevMode);
});
document.getElementById("btn-error-reset")!.addEventListener("click", () => void setModeOnServer(MODE_START));
document.getElementById("cam-toggle")!.addEventListener("click", toggleCamera);
document.getElementById("btn-difficulty")!.addEventListener("click", async () => {
  const elo = parseInt((document.getElementById("difficulty-elo") as HTMLInputElement).value, 10);
  if (!isNaN(elo)) await cmdSetDifficulty(elo);
});
document.getElementById("difficulty-elo")!.addEventListener("keydown", async (e) => {
  if ((e as KeyboardEvent).key === "Enter") {
    const elo = parseInt((e.target as HTMLInputElement).value, 10);
    if (!isNaN(elo)) await cmdSetDifficulty(elo);
  }
});
document.querySelectorAll(".inline-error").forEach((pop) => {
  pop.addEventListener("click", () => {
    if (pop.id === "go-error") dismissInlineError("go");
    if (pop.id === "move-error") dismissInlineError("move");
  });
});
(document.getElementById("go-n") as HTMLInputElement).addEventListener("input", (e) => {
  const input = e.target as HTMLInputElement;
  input.value = input.value.replace(/[^0-9]/g, "");
});
["move-from", "move-to"].forEach((id) => {
  (document.getElementById(id) as HTMLInputElement).addEventListener("input", (e) => {
    const input = e.target as HTMLInputElement;
    input.value = input.value.toLowerCase();
  });
});

// ── Init ───────────────────────────────────────────────────────────────────

companion.init({
  onStartGame: async () => { await setModeOnServer(MODE_VS_HUMAN); },
  onStartSelfPlay: async () => { await setModeOnServer(MODE_VS_SELF); },
  onWipe: () => void cmdMaintenance("wipe", true),
  onReset: () => void cmdMaintenance("reset", true),
});

if (new URLSearchParams(window.location.search).has("compact")) {
  document.querySelector(".app")?.classList.remove("kiosk");
}

if (mockMode) {
  seedMockState();
} else {
  loadPersistedState();
}
renderBoard();
renderMaterial();
renderTape();
renderTopStatus();
renderEvalBar();
renderModePanel();

if (mockMode) {
  setStatus("mock", "warn");
  document.getElementById("board-loading")?.classList.add("hidden");
  const machineEl = document.getElementById("machine-name");
  if (machineEl) machineEl.textContent = "mock";
  // ?mode=N previews the mode control / fault banner without a robot.
  const mockModeParam = parseInt(new URLSearchParams(window.location.search).get("mode") ?? "", 10);
  if (!isNaN(mockModeParam)) updateMode(mockModeParam);
  const mockActive = gameActive();
  const companionScenario = new URLSearchParams(window.location.search).get("companion");
  if (companionScenario === "won") {
    companion.onInit(plyCount, mockActive, 0, "white-won");
  } else if (companionScenario === "lost") {
    companion.onInit(plyCount, mockActive, 0, "black-won");
  } else if (companionScenario === "draw") {
    companion.onInit(plyCount, mockActive, 0, "draw");
  } else if (companionScenario === "bad-state") {
    companion.onInit(plyCount, mockActive, 4, "");
    companion.forceScenario("bad-state");
  } else if (companionScenario === "needs-fix") {
    companion.onInit(plyCount, mockActive, 0, "");
    companion.forceScenario("needs-fix");
  } else if (companionScenario === "welcome") {
    companion.onInit(0, false, 0, "");
  } else if (companionScenario === "first-move") {
    companion.onInit(0, false, 0, "");
    setTimeout(() => companion.onMove(1), 100);
  } else if (companionScenario === "first-capture") {
    companion.onInit(plyCount, mockActive, 0, "");
    companion.onFirstCapture();
  } else if (companionScenario === "in-check") {
    companion.onInit(plyCount, mockActive, 0, "", false);
    companion.onSnapshot(plyCount, mockActive, 0, "", true);
  } else {
    companion.onInit(plyCount, mockActive, 0, "");
  }
} else {
  connect()
    .then(async () => {
      try {
        const cfg = await doCommand({ "companion-config": true });
        companion.configure({
          badStateDelayMs:      typeof cfg.bad_state_delay_ms === "number" ? cfg.bad_state_delay_ms : undefined,
          welcomeReviveMs:      typeof cfg.welcome_revive_ms === "number" ? cfg.welcome_revive_ms : undefined,
          inCheckDismissMs:     typeof cfg.in_check_dismiss_ms === "number" ? cfg.in_check_dismiss_ms : undefined,
          firstMoveDismissMs:   typeof cfg.first_move_dismiss_ms === "number" ? cfg.first_move_dismiss_ms : undefined,
        });
      } catch {}
    })
    .then(refreshState)
    .then(() => { startAutoRefresh(); startIdleWatch(); })
    .catch((e) => {
      const msg = e instanceof Error ? e.message : String(e);
      setStatus("offline", "err");
      pushEvent("err", msg);
    });
}
