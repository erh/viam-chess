// companion.ts — Garry companion overlay system.
// Manages instructional modals, speech bubbles, and persistent indicators.

// ── Types ──────────────────────────────────────────────────────────────────

export type GameOutcome = "white-won" | "black-won" | "draw" | "";
type Scenario = "welcome" | "first-move" | "first-capture" | "in-check" | "bad-state" | "won" | "lost" | "draw" | "long-pause";
type Mood = "welcome" | "positive" | "neutral" | "sad" | "warn" | "idle";

interface CopyItem {
  eyebrow: string;
  title: string;
  body?: string;
  bullets?: string[];
}

export interface CompanionCallbacks {
  onAutoEnable: () => Promise<void>;
  onAutoDisable: () => Promise<void>;
  onWipe: () => void;
  onReset: () => void;
}

export interface CompanionOptions {
  badStateDelayMs?: number;       // appear + revive (default 60 s)
  welcomeReviveMs?: number;       // re-show welcome after dismiss (default 60 s)
  inCheckDismissMs?: number;      // auto-hide in-check bubble (default 8 s)
  firstMoveDismissMs?: number;    // auto-hide first-move bubble (default 8 s)
  longPauseTriggerMs?: number;    // idle before long-pause modal (default 120 s)
  longPauseRateLimitMs?: number;  // cooldown before re-triggering long-pause (default 240 s)
}

// ── Copy pools (voice: Garry, chess master) ───────────────────────────────

const COPY: Record<Scenario, CopyItem[]> = {
  welcome: [
    {
      eyebrow: "Hello, Human",
      title: "I\u2019m Garry \u2014 your robot opponent.",
      bullets: [
        "You play white. Push a piece on the physical board, or drag one here.",
        "The arm next to you is mine. I\u2019ll move my pieces with it on my turn.",
        "Watch the TAPE on the right \u2014 every move shows up there in order.",
        "Flip AUTO on whenever you\u2019re ready. White moves first.",
      ],
    },
  ],
  "first-move": [
    { eyebrow: "Nice", title: "You\u2019re in.", body: "Watch the TAPE on the right \u2014 that\u2019s the running record." },
    { eyebrow: "Game on", title: "And we\u2019re off.", body: "Every move shows up on the TAPE. The arm\u2019s thinking." },
    { eyebrow: "Opening move", title: "A bold start.", body: "The TAPE on the right tracks every move from here." },
  ],
  "in-check": [
    { eyebrow: "Watch out", title: "You\u2019re in check.", body: "Your king is under attack. You must move out of check." },
    { eyebrow: "Check", title: "Your king is threatened.", body: "Get your king to safety before anything else." },
    { eyebrow: "Check", title: "In check.", body: "You\u2019ll need to address that before playing any other move." },
  ],
  "first-capture": [
    {
      eyebrow: "Nice capture",
      title: "Place it in the corner.",
      bullets: [
        "Set captured black pieces outside the board near h1.",
        "Captured white pieces go near a8.",
        "Extra promotion queens live there too \u2014 the arm knows to find them.",
      ],
    },
    {
      eyebrow: "Got one",
      title: "Tuck it in the corner.",
      bullets: [
        "Captured black pieces go near h1, outside the board.",
        "Captured white pieces go near a8.",
        "Spare queens for promotion live in those corners too.",
      ],
    },
  ],
  "bad-state": [
    {
      eyebrow: "Board mismatch",
      title: "Something\u2019s out of place.",
      bullets: [
        "Reset physical pieces to the starting position.",
        "Extra promotion queens: white\u2019s near a8, black\u2019s near h1.",
        "Then hit \u201cWipe state\u201d to clear my memory.",
      ],
    },
    {
      eyebrow: "Pieces don\u2019t match",
      title: "I\u2019m a little confused.",
      bullets: [
        "Put all pieces back to their starting squares.",
        "Spare promotion queens belong near a8 (white) and h1 (black).",
        "Then wipe my state so we can start fresh.",
      ],
    },
  ],
  won: [
    { eyebrow: "Checkmate", title: "You beat me.", body: "Well played. Reset the board for a rematch \u2014 I\u2019ll go pack up the pieces myself." },
    { eyebrow: "Victory", title: "That was excellent.", body: "Hats off. Hit Reset and the arm will tidy up for round two." },
    { eyebrow: "GG", title: "You got me.", body: "Solid game. Reset and I\u2019ll set the board back myself." },
  ],
  lost: [
    { eyebrow: "Checkmate", title: "That\u2019s mate.", body: "Good game. Reset the board for another round \u2014 I\u2019ll go easy. Probably." },
    { eyebrow: "I win", title: "Mate.", body: "Tough one. The arm can reset the board if you want a rematch." },
    { eyebrow: "Checkmate", title: "Better luck next time.", body: "Don\u2019t take it personally. Reset to play again \u2014 I\u2019ll handle the pieces." },
  ],
  draw: [
    { eyebrow: "Stalemate", title: "A draw.", body: "Neither of us could finish it. Reset to play again." },
    { eyebrow: "Draw", title: "Even hands.", body: "No winner this round. Reset and we\u2019ll go again." },
  ],
  "long-pause": [
    { eyebrow: "Still there?", title: "I\u2019ll wait.", body: "No moves for a while. Take your time." },
    { eyebrow: "No rush", title: "Take your time.", body: "The board\u2019s yours when you\u2019re back." },
    { eyebrow: "Quiet", title: "Hello?", body: "Let me know when you\u2019re ready to continue." },
  ],
};

const MOOD: Record<Scenario, Mood> = {
  welcome: "welcome",
  "first-move": "positive",
  "first-capture": "neutral",
  "in-check": "warn",
  "bad-state": "warn",
  won: "positive",
  lost: "sad",
  draw: "neutral",
  "long-pause": "idle",
};

const BLOCKING: Record<Scenario, boolean> = {
  welcome: true,
  "first-move": false,
  "first-capture": true,
  "in-check": false,
  "bad-state": true,
  won: true,
  lost: true,
  draw: true,
  "long-pause": true,
};

function moodColor(mood: Mood): string {
  return (
    {
      welcome: "var(--accent)",
      idle: "var(--text-3)",
      positive: "var(--accent)",
      neutral: "var(--text-2)",
      sad: "var(--text-3)",
      warn: "var(--warning)",
    } as Record<Mood, string>
  )[mood];
}

// ── Module state ───────────────────────────────────────────────────────────

let cbs: CompanionCallbacks | null = null;
let initialized = false;
let badStateDelayMs      = 60_000;
let welcomeReviveMs      = 60_000;
let inCheckDismissMs     = 8_000;
let firstMoveDismissMs   = 8_000;
let longPauseTriggerMs   = 2 * 60_000;
let longPauseRateLimitMs = 4 * 60_000;

let activeScenario: Scenario | null = null;
let activeCopy: CopyItem | null = null;

let badStateMinimized = false;
let badStateReviveTimer: ReturnType<typeof setTimeout> | null = null;
let badStateAppearTimer: ReturnType<typeof setTimeout> | null = null;

let welcomeDismissedCount = 0;
let welcomeRevivedOnce = false;
let welcomeAutoOn = false;
let welcomeReviveTimer: ReturnType<typeof setTimeout> | null = null;

let firstMoveBubbleDone = false;
let firstMoveAutoDismissTimer: ReturnType<typeof setTimeout> | null = null;

let firstCaptureDone = false;
let firstCapturePlyCount = -1;

let inCheckActive = false;
let inCheckAutoDismissTimer: ReturnType<typeof setTimeout> | null = null;

let gameEndScenario: "won" | "lost" | "draw" | null = null;
let gameEndDismissed = false;

let longPauseRateLimited = false;
let longPauseTimer: ReturnType<typeof setTimeout> | null = null;


let extPlyCount = 0;
let extAutoMode = false;
let extMismatchCount = 0;
let extOutcome: GameOutcome = "";
let extInCheck = false;

// ── Public API ─────────────────────────────────────────────────────────────

export function init(callbacks: CompanionCallbacks): void {
  cbs = callbacks;
  injectStyles();
  ensureRoot();
}

export function configure(options: CompanionOptions): void {
  if (options.badStateDelayMs      !== undefined) badStateDelayMs      = options.badStateDelayMs;
  if (options.welcomeReviveMs      !== undefined) welcomeReviveMs      = options.welcomeReviveMs;
  if (options.inCheckDismissMs     !== undefined) inCheckDismissMs     = options.inCheckDismissMs;
  if (options.firstMoveDismissMs   !== undefined) firstMoveDismissMs   = options.firstMoveDismissMs;
  if (options.longPauseTriggerMs   !== undefined) longPauseTriggerMs   = options.longPauseTriggerMs;
  if (options.longPauseRateLimitMs !== undefined) longPauseRateLimitMs = options.longPauseRateLimitMs;
}

export function onInit(plyCount: number, autoMode: boolean, mismatchCount: number, outcome: GameOutcome, inCheck = false): void {
  extPlyCount = plyCount;
  extAutoMode = autoMode;
  extMismatchCount = mismatchCount;
  extOutcome = outcome;
  extInCheck = inCheck;
  initialized = true;

  if (outcome) {
    const sc = outcomeToScenario(outcome);
    if (sc) {
      gameEndScenario = sc;
      gameEndDismissed = false;
      setActiveScenario(sc);
    }
  } else if (plyCount === 0) {
    setActiveScenario("welcome");
    scheduleWelcomeRevive();
    if (mismatchCount > 0) {
      badStateAppearTimer = setTimeout(() => {
        badStateAppearTimer = null;
        if (extMismatchCount > 0 && activeScenario !== "bad-state" && !badStateMinimized) {
          setActiveScenario("bad-state");
          scheduleBadStateRevive();
          render();
        }
      }, badStateDelayMs);
    }
  } else if (mismatchCount > 0) {
    badStateAppearTimer = setTimeout(() => {
      badStateAppearTimer = null;
      if (extMismatchCount > 0 && activeScenario !== "bad-state" && !badStateMinimized) {
        setActiveScenario("bad-state");
        scheduleBadStateRevive();
        render();
      }
    }, badStateDelayMs);
  }

  scheduleLongPause();
  render();
}

export function onSnapshot(plyCount: number, autoMode: boolean, mismatchCount: number, outcome: GameOutcome, inCheck = false): void {
  if (!initialized) return;

  const prevOutcome = extOutcome;
  const prevMismatchCount = extMismatchCount;
  const prevPlyCount = extPlyCount;
  const prevInCheck = extInCheck;

  extPlyCount = plyCount;
  extAutoMode = autoMode;
  extMismatchCount = mismatchCount;
  extOutcome = outcome;
  extInCheck = inCheck;

  let dirty = false;

  // New game end
  if (!prevOutcome && outcome) {
    const sc = outcomeToScenario(outcome);
    if (sc) {
      gameEndScenario = sc;
      gameEndDismissed = false;
      setActiveScenario(sc);
      dirty = true;
    }
  }

  // Welcome auto-dismiss on first move
  if (activeScenario === "welcome" && plyCount > 0) {
    doWelcomeDismiss(true);
    dirty = true;
  }

  // First-capture auto-dismiss on any next move
  if (activeScenario === "first-capture" && plyCount > firstCapturePlyCount) {
    activeScenario = null;
    dirty = true;
  }

  // Long-pause auto-dismiss on move
  if (activeScenario === "long-pause" && plyCount > prevPlyCount) {
    activeScenario = null;
    scheduleLongPause();
    dirty = true;
  }

  // Bad-state management (no game end)
  if (!outcome) {
    if (mismatchCount > 0 && activeScenario !== "bad-state" && !badStateMinimized && !badStateAppearTimer) {
      badStateAppearTimer = setTimeout(() => {
        badStateAppearTimer = null;
        if (extMismatchCount > 0 && activeScenario !== "bad-state" && !badStateMinimized) {
          setActiveScenario("bad-state");
          scheduleBadStateRevive();
          render();
        }
      }, badStateDelayMs);
    } else if (mismatchCount === 0) {
      if (badStateAppearTimer) { clearTimeout(badStateAppearTimer); badStateAppearTimer = null; }
      if (activeScenario === "bad-state" || badStateMinimized) {
        clearBadState();
        dirty = true;
      }
    }
  }

  // In-check bubble: fires on rising edge (false → true), auto-dismisses in 8s
  if (inCheck && !prevInCheck && !outcome && !(activeScenario && BLOCKING[activeScenario])) {
    if (inCheckAutoDismissTimer) { clearTimeout(inCheckAutoDismissTimer); inCheckAutoDismissTimer = null; }
    inCheckActive = true;
    setActiveScenario("in-check");
    inCheckAutoDismissTimer = setTimeout(() => {
      inCheckAutoDismissTimer = null;
      if (activeScenario === "in-check") { activeScenario = null; inCheckActive = false; render(); }
    }, inCheckDismissMs);
    dirty = true;
  } else if (!inCheck && inCheckActive) {
    // Check was resolved — dismiss immediately
    if (inCheckAutoDismissTimer) { clearTimeout(inCheckAutoDismissTimer); inCheckAutoDismissTimer = null; }
    if (activeScenario === "in-check") { activeScenario = null; }
    inCheckActive = false;
    dirty = true;
  }

  if (dirty) render();
}

export function onMove(newPlyCount: number): void {
  const wasZero = extPlyCount === 0;
  extPlyCount = newPlyCount;

  if (activeScenario === "welcome") {
    doWelcomeDismiss(true); // auto-dismiss — doesn't count as user-dismissed
  }

  // First-move bubble: show on first ply regardless of whether welcome was manually dismissed
  if (wasZero && newPlyCount > 0 && !firstMoveBubbleDone) {
    setActiveScenario("first-move");
    startFirstMoveAutoDismiss();
  }

  if (activeScenario === "first-capture" && newPlyCount > firstCapturePlyCount) {
    activeScenario = null;
  }
  if (activeScenario === "long-pause") {
    activeScenario = null;
  }
  if (activeScenario === "in-check") {
    if (inCheckAutoDismissTimer) { clearTimeout(inCheckAutoDismissTimer); inCheckAutoDismissTimer = null; }
    activeScenario = null;
    inCheckActive = false;
  }

  clearLongPauseTimer();
  if (newPlyCount > 0 && !extOutcome) scheduleLongPause();

  render();
}

export function onReset(): void {
  activeScenario = null;
  activeCopy = null;
  badStateMinimized = false;
  if (badStateReviveTimer) { clearTimeout(badStateReviveTimer); badStateReviveTimer = null; }
  if (badStateAppearTimer) { clearTimeout(badStateAppearTimer); badStateAppearTimer = null; }
  gameEndScenario = null;
  gameEndDismissed = false;
  welcomeDismissedCount = 0;
  welcomeRevivedOnce = false;
  welcomeAutoOn = false;
  if (welcomeReviveTimer) { clearTimeout(welcomeReviveTimer); welcomeReviveTimer = null; }
  firstMoveBubbleDone = false;
  firstCaptureDone = false;
  firstCapturePlyCount = -1;
  if (inCheckAutoDismissTimer) { clearTimeout(inCheckAutoDismissTimer); inCheckAutoDismissTimer = null; }
  inCheckActive = false;
  extInCheck = false;
  longPauseRateLimited = false;
  clearLongPauseTimer();
  extPlyCount = 0;
  extOutcome = "";
  extMismatchCount = 0;

  setActiveScenario("welcome");
  scheduleWelcomeRevive();
  render();
}

export function onActivity(): void {
  if (activeScenario === "long-pause") {
    activeScenario = null;
    scheduleLongPause();
    render();
  }
}

export function forceScenario(sc: string): void {
  setActiveScenario(sc as Scenario);
  render();
}

export function onAutoToggle(enabled: boolean): void {
  extAutoMode = enabled;
  const changed = welcomeAutoOn !== enabled;
  welcomeAutoOn = enabled;
  if (enabled && welcomeReviveTimer) {
    clearTimeout(welcomeReviveTimer);
    welcomeReviveTimer = null;
  }
  if (activeScenario === "welcome" && changed) {
    // Surgical update — avoid full re-render (root.innerHTML="" causes a visible flash
    // even when the animation is suppressed, because the browser can paint the empty state)
    const box = document.querySelector<HTMLElement>("#companion-root .auto-cb-box");
    const msg = document.querySelector<HTMLElement>("#companion-root .auto-ready-msg");
    if (box) {
      box.style.background = enabled ? "#0e0f10" : "transparent";
      box.textContent = enabled ? "\u2713" : "";
    }
    if (msg) {
      msg.style.display = enabled ? "flex" : "none";
      if (enabled) msg.style.animation = "cp-fade-in 220ms ease-out";
    }
  }
}

export function onFirstCapture(): void {
  if (firstCaptureDone || extOutcome) return;
  firstCaptureDone = true;
  firstCapturePlyCount = extPlyCount;
  setActiveScenario("first-capture");
  render();
}

// ── State helpers ──────────────────────────────────────────────────────────

function setActiveScenario(sc: Scenario): void {
  activeScenario = sc;
  activeCopy = pickCopy(sc);
}

function pickCopy(scenario: Scenario): CopyItem {
  const pool = COPY[scenario];
  return pool[Math.floor(Math.random() * pool.length)];
}

function outcomeToScenario(o: GameOutcome): "won" | "lost" | "draw" | null {
  if (o === "white-won") return "won";
  if (o === "black-won") return "lost";
  if (o === "draw") return "draw";
  return null;
}

function doWelcomeDismiss(auto = false): void {
  if (activeScenario === "welcome") activeScenario = null;
  welcomeDismissedCount++;
  welcomeAutoOn = false;
  if (welcomeReviveTimer) { clearTimeout(welcomeReviveTimer); welcomeReviveTimer = null; }
  if (!auto) scheduleWelcomeRevive();
}

function clearBadState(): void {
  if (activeScenario === "bad-state") activeScenario = null;
  badStateMinimized = false;
  if (badStateReviveTimer) { clearTimeout(badStateReviveTimer); badStateReviveTimer = null; }
  if (badStateAppearTimer) { clearTimeout(badStateAppearTimer); badStateAppearTimer = null; }
}

function scheduleWelcomeRevive(): void {
  if (welcomeReviveTimer) clearTimeout(welcomeReviveTimer);
  if (welcomeRevivedOnce || extPlyCount > 0 || extAutoMode || extOutcome) return;
  welcomeReviveTimer = setTimeout(() => {
    welcomeReviveTimer = null;
    if (!welcomeRevivedOnce && extPlyCount === 0 && !extAutoMode && !activeScenario && !extOutcome) {
      welcomeRevivedOnce = true;
      setActiveScenario("welcome");
      render();
    }
  }, welcomeReviveMs);
}

function scheduleBadStateRevive(): void {
  if (badStateReviveTimer) clearTimeout(badStateReviveTimer);
  badStateReviveTimer = setTimeout(() => {
    badStateReviveTimer = null;
    if (badStateMinimized && extMismatchCount > 0) {
      badStateMinimized = false;
      setActiveScenario("bad-state");
      render();
      scheduleBadStateRevive();
    }
  }, 5 * badStateDelayMs);
}

function scheduleLongPause(): void {
  clearLongPauseTimer();
  if (extPlyCount === 0 || extOutcome || longPauseRateLimited) return;
  longPauseTimer = setTimeout(() => {
    longPauseTimer = null;
    if (extPlyCount > 0 && !extOutcome && !longPauseRateLimited) {
      longPauseRateLimited = true;
      setActiveScenario("long-pause");
      render();
      setTimeout(() => { longPauseRateLimited = false; }, longPauseRateLimitMs);
    }
  }, longPauseTriggerMs);
}

function clearLongPauseTimer(): void {
  if (longPauseTimer) { clearTimeout(longPauseTimer); longPauseTimer = null; }
}

function startFirstMoveAutoDismiss(): void {
  if (firstMoveAutoDismissTimer) clearTimeout(firstMoveAutoDismissTimer);
  firstMoveAutoDismissTimer = setTimeout(() => {
    firstMoveAutoDismissTimer = null;
    if (activeScenario === "first-move") {
      activeScenario = null;
      firstMoveBubbleDone = true;
      render();
    }
  }, firstMoveDismissMs);
}

// ── Style injection ────────────────────────────────────────────────────────

function injectStyles(): void {
  if (document.getElementById("companion-styles")) return;
  const s = document.createElement("style");
  s.id = "companion-styles";
  s.textContent = `
@keyframes cp-fade-in {
  from { opacity: 0; }
  to   { opacity: 1; }
}
@keyframes cp-bubble-in {
  from { opacity: 0; transform: translateY(-8px) scale(0.96); }
  to   { opacity: 1; transform: translateY(0)    scale(1); }
}
@keyframes cp-takeover-in {
  from { opacity: 0; transform: scale(0.92); }
  to   { opacity: 1; transform: scale(1); }
}
@keyframes cp-knight-bounce {
  0%   { transform: scale(0.4) rotate(-30deg); opacity: 0; }
  60%  { transform: scale(1.15) rotate(8deg);  opacity: 1; }
  100% { transform: scale(1)    rotate(0deg);  opacity: 1; }
}
@keyframes cp-fall {
  0%   { transform: translateY(0)     rotate(0deg);   opacity: 0;   }
  10%  {                                              opacity: 0.9; }
  100% { transform: translateY(120vh) rotate(720deg); opacity: 0.5; }
}
@keyframes cp-pill-pulse {
  0%, 100% { opacity: 1;    box-shadow: 0 0 0 0   rgba(224,178,96,0.4); }
  50%       { opacity: 0.85; box-shadow: 0 0 0 6px rgba(224,178,96,0); }
}
@keyframes cp-sparkle {
  0%, 100% { opacity: 0.2; transform: scale(0.8); }
  50%      { opacity: 0.8; transform: scale(1.1); }
}
@keyframes cp-float {
  0%, 100% { transform: translateX(-50%) translateY(0px); }
  50%      { transform: translateX(-50%) translateY(-6px); }
}
@keyframes cp-drift {
  0%   { transform: translateY(8px)  rotate(-4deg); opacity: 0.2; }
  50%  {                                            opacity: 0.8; }
  100% { transform: translateY(-12px) rotate(8deg); opacity: 0; }
}
@keyframes cp-scan {
  0%   { transform: translateY(0);    opacity: 0; }
  8%   { opacity: 1; }
  92%  { opacity: 1; }
  100% { transform: translateY(300px); opacity: 0; }
}
@keyframes cp-slot-pulse {
  0%, 100% { opacity: 0.7; box-shadow: 0 0 10px rgba(127,209,168,0.3) inset; }
  50%      { opacity: 1;   box-shadow: 0 0 20px rgba(127,209,168,0.65) inset; }
}
@keyframes cp-cell-float {
  0%, 100% { transform: translateY(0px); }
  50%      { transform: translateY(-3px); }
}
#companion-root { position: fixed; inset: 0; pointer-events: none; z-index: 100; }
#companion-root * { box-sizing: border-box; }
`;
  document.head.appendChild(s);
}

// ── DOM helpers ────────────────────────────────────────────────────────────

function ensureRoot(): HTMLElement {
  let root = document.getElementById("companion-root");
  if (!root) {
    root = document.createElement("div");
    root.id = "companion-root";
    document.body.appendChild(root);
  }
  return root;
}

type StyleMap = Partial<CSSStyleDeclaration> & { [k: string]: string };

function h<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  styles: StyleMap | null = null,
  ...children: (HTMLElement | Text | string)[]
): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  if (styles) Object.assign(el.style, styles);
  for (const c of children) {
    if (typeof c === "string") el.appendChild(document.createTextNode(c));
    else el.appendChild(c);
  }
  return el;
}

function css(el: HTMLElement, styles: StyleMap): HTMLElement {
  Object.assign(el.style, styles);
  return el;
}

function dismissX(onDismiss: () => void): HTMLButtonElement {
  const btn = h("button", {
    position: "absolute",
    top: "12px",
    right: "12px",
    width: "24px",
    height: "24px",
    background: "none",
    border: "none",
    color: "var(--text-3)",
    cursor: "pointer",
    fontSize: "20px",
    lineHeight: "1",
    display: "grid",
    placeItems: "center",
    zIndex: "5",
    pointerEvents: "auto",
  }, "\u00d7");
  btn.setAttribute("aria-label", "dismiss");
  btn.addEventListener("click", onDismiss);
  return btn;
}

// ── Dioramas ───────────────────────────────────────────────────────────────

function dioramaWelcome(size: number, color: string): HTMLElement {
  const wrap = h("div", { position: "relative", width: `${size}px`, height: `${size}px` });
  const sparkles = [
    { top: "12%", left: "20%", s: 14 }, { top: "20%", left: "78%", s: 10 },
    { top: "38%", left: "85%", s: 16 }, { top: "32%", left: "8%",  s: 12 },
    { top: "62%", left: "92%", s: 9  }, { top: "70%", left: "12%", s: 11 },
  ];
  sparkles.forEach((p, i) => {
    const sp = h("span", {
      position: "absolute", top: p.top, left: p.left,
      fontSize: `${p.s}px`, color: "var(--text-1)", opacity: "0.55",
      animation: `cp-sparkle 2.4s ease-in-out ${i * 0.3}s infinite`,
    }, "\u2726");
    wrap.appendChild(sp);
  });
  const crown = h("span", {
    position: "absolute", top: "8%", left: "50%",
    transform: "translateX(-50%)",
    fontSize: `${size * 0.14}px`, color: "var(--warning)",
    animation: "cp-float 2.6s ease-in-out infinite",
    textShadow: "0 4px 12px rgba(224,178,96,0.4)",
  }, "\u2654");
  const knight = h("span", {
    position: "absolute", top: "50%", left: "50%",
    transform: "translate(-50%, -50%) rotate(-12deg)",
    fontSize: `${size * 0.7}px`, color, lineHeight: "1",
    textShadow: "0 12px 36px rgba(0,0,0,0.55)",
  }, "\u265e");
  wrap.appendChild(crown);
  wrap.appendChild(knight);
  return wrap;
}

function dioramaBadState(size: number, color: string): HTMLElement {
  const wrap = h("div", { position: "relative", width: `${size}px`, height: `${size}px` });
  const pawns = [
    { top: "74%", left: "18%", rot: -42, s: 0.18 },
    { top: "78%", left: "76%", rot: 28,  s: 0.16 },
    { top: "24%", left: "80%", rot: 90,  s: 0.14 },
    { top: "30%", left: "14%", rot: -110, s: 0.20 },
  ];
  pawns.forEach(p => {
    wrap.appendChild(h("span", {
      position: "absolute", top: p.top, left: p.left,
      fontSize: `${size * p.s}px`, color: "var(--text-3)",
      transform: `rotate(${p.rot}deg)`, opacity: "0.7",
    }, "\u265f"));
  });
  wrap.appendChild(h("span", {
    position: "absolute", top: "14%", left: "58%",
    fontSize: `${size * 0.18}px`, color,
    fontFamily: "var(--font-display)", fontWeight: "700",
    animation: "cp-float 2s ease-in-out infinite",
    textShadow: "0 4px 12px rgba(224,178,96,0.4)",
  }, "?"));
  // Scanning line — sweeps top to bottom as if camera diagnosing
  wrap.appendChild(h("div", {
    position: "absolute", top: "0", left: "0", right: "0", height: "2px",
    background: `linear-gradient(to right, transparent, ${color}, transparent)`,
    boxShadow: `0 0 12px ${color}`,
    animation: "cp-scan 3s ease-in-out infinite",
  }));
  wrap.appendChild(h("span", {
    position: "absolute", top: "52%", left: "50%",
    transform: "translate(-50%, -50%) rotate(95deg)",
    fontSize: `${size * 0.62}px`, color,
    textShadow: "0 12px 36px rgba(0,0,0,0.55)",
  }, "\u265e"));
  return wrap;
}

function dioramaWon(size: number, color: string): HTMLElement {
  const wrap = h("div", { position: "relative", width: `${size}px`, height: `${size}px` });
  wrap.appendChild(h("span", {
    position: "absolute", bottom: "18%", left: "38%",
    fontSize: `${size * 0.18}px`, color: "var(--warning)",
    transform: "rotate(70deg)",
    textShadow: "0 4px 12px rgba(0,0,0,0.4)",
  }, "\u2654"));
  wrap.appendChild(h("span", {
    position: "absolute", top: "46%", left: "54%",
    transform: "translate(-50%, -50%) rotate(35deg)",
    fontSize: `${size * 0.66}px`, color,
    textShadow: "0 12px 36px rgba(0,0,0,0.55)",
  }, "\u265e"));
  [
    { top: "12%", left: "24%", s: 10 }, { top: "8%",  left: "70%", s: 12 },
    { top: "24%", left: "88%", s: 8  }, { top: "20%", left: "10%", s: 14 },
  ].forEach((p, i) => {
    wrap.appendChild(h("span", {
      position: "absolute", top: p.top, left: p.left,
      fontSize: `${p.s}px`, color: "var(--accent)", opacity: "0.7",
      animation: `cp-sparkle 1.8s ease-in-out ${i * 0.25}s infinite`,
    }, "\u2726"));
  });
  return wrap;
}

function dioramaLost(size: number, color: string): HTMLElement {
  const wrap = h("div", { position: "relative", width: `${size}px`, height: `${size}px` });
  wrap.appendChild(h("span", {
    position: "absolute", top: "10%", left: "50%",
    transform: "translateX(-50%)",
    fontSize: `${size * 0.18}px`, color: "var(--warning)",
    animation: "cp-float 2.4s ease-in-out infinite",
    textShadow: "0 4px 12px rgba(224,178,96,0.5)",
  }, "\u2654"));
  [{ left: "18%", rot: -60 }, { left: "72%", rot: 50 }, { left: "40%", rot: 110 }].forEach(p => {
    wrap.appendChild(h("span", {
      position: "absolute", bottom: "12%", left: p.left,
      fontSize: `${size * 0.13}px`, color: "var(--text-4)",
      transform: `rotate(${p.rot}deg)`, opacity: "0.6",
    }, "\u2659"));
  });
  wrap.appendChild(h("span", {
    position: "absolute", top: "54%", left: "50%",
    transform: "translate(-50%, -50%) rotate(-4deg)",
    fontSize: `${size * 0.66}px`, color,
    textShadow: "0 12px 36px rgba(0,0,0,0.55)",
  }, "\u265e"));
  return wrap;
}

function dioramaDraw(size: number, color: string): HTMLElement {
  const wrap = h("div", { position: "relative", width: `${size}px`, height: `${size}px` });
  wrap.appendChild(h("span", {
    position: "absolute", top: "50%", left: "24%",
    transform: "translate(-50%, -50%) scaleX(-1)",
    fontSize: `${size * 0.5}px`, color,
    textShadow: "0 8px 24px rgba(0,0,0,0.5)",
  }, "\u265e"));
  wrap.appendChild(h("span", {
    position: "absolute", top: "50%", left: "76%",
    transform: "translate(-50%, -50%)",
    fontSize: `${size * 0.5}px`, color,
    textShadow: "0 8px 24px rgba(0,0,0,0.5)",
  }, "\u265e"));
  wrap.appendChild(h("span", {
    position: "absolute", top: "46%", left: "50%",
    transform: "translate(-50%, -50%)",
    fontSize: `${size * 0.16}px`, color: "var(--text-3)",
    fontFamily: "var(--font-mono)", fontWeight: "600",
  }, "="));
  return wrap;
}

function dioramaPause(size: number, color: string): HTMLElement {
  const wrap = h("div", { position: "relative", width: `${size}px`, height: `${size}px` });
  [
    { top: "14%", left: "60%", s: 0.10, d: 0   },
    { top: "24%", left: "70%", s: 0.13, d: 0.5 },
    { top: "36%", left: "78%", s: 0.16, d: 1.0 },
  ].forEach(p => {
    wrap.appendChild(h("span", {
      position: "absolute", top: p.top, left: p.left,
      fontSize: `${size * p.s}px`, color: "var(--text-2)",
      fontFamily: "var(--font-display)", fontWeight: "600",
      fontStyle: "italic", opacity: "0.7",
      animation: `cp-drift 3s ease-in-out ${p.d}s infinite`,
    }, "z"));
  });
  wrap.appendChild(h("span", {
    position: "absolute", top: "54%", left: "46%",
    transform: "translate(-50%, -50%) rotate(18deg)",
    fontSize: `${size * 0.62}px`, color,
    textShadow: "0 10px 30px rgba(0,0,0,0.5)",
  }, "\u265e"));
  return wrap;
}

function dioramaFirstCapture(size: number): HTMLElement {
  const accent = "var(--accent)";
  const boardW = size * 0.72;
  const boardH = size * 0.62;
  const cell = boardW / 8;
  const boardLeft = (size - boardW) / 2;
  const boardTop  = (size - boardH) / 2;

  const wrap = h("div", { position: "relative", width: `${size}px`, height: `${size}px` });

  // Halo
  wrap.appendChild(h("div", {
    position: "absolute", top: "50%", left: "50%",
    transform: "translate(-50%, -50%)",
    width: `${size * 0.82}px`, height: `${size * 0.82}px`, borderRadius: "50%",
    background: "radial-gradient(closest-side, rgba(127,209,168,0.22) 0%, transparent 75%)",
    filter: "blur(2px)",
  }));

  // Board plate (overflow visible so graveyard columns extend outside)
  const plate = h("div", {
    position: "absolute",
    left: `${boardLeft}px`, top: `${boardTop}px`,
    width: `${boardW}px`, height: `${boardH}px`,
    overflow: "visible",
    background: "var(--bg-2)",
    border: "1px solid var(--line-2)",
    boxShadow: "0 24px 48px rgba(0,0,0,0.55)",
  });

  // 8×8 checkerboard
  const grid = h("div", {
    position: "absolute", inset: "0",
    display: "grid",
    gridTemplateColumns: "repeat(8, 1fr)",
    gridTemplateRows: "repeat(8, 1fr)",
  });
  for (let i = 0; i < 64; i++) {
    const r = Math.floor(i / 8), c = i % 8;
    grid.appendChild(h("div", { background: (r + c) % 2 === 1 ? "rgba(255,255,255,0.05)" : "transparent" }));
  }
  plate.appendChild(grid);

  // h1 glow slot — right of h-file, bottom row
  plate.appendChild(h("div", {
    position: "absolute", right: `${-cell}px`, bottom: "0",
    width: `${cell}px`, height: `${cell}px`, boxSizing: "border-box",
    background: "radial-gradient(closest-side, rgba(127,209,168,0.55), rgba(127,209,168,0.18))",
    border: `1px solid ${accent}`,
    animation: "cp-slot-pulse 3s ease-in-out infinite",
  }));

  // a8 glow slot — left of a-file, top row
  plate.appendChild(h("div", {
    position: "absolute", left: `${-cell}px`, top: "0",
    width: `${cell}px`, height: `${cell}px`, boxSizing: "border-box",
    background: "radial-gradient(closest-side, rgba(127,209,168,0.4), rgba(127,209,168,0.12))",
    border: `1px solid ${accent}`,
    animation: "cp-slot-pulse 3s ease-in-out 1.2s infinite",
  }));

  // Coordinate labels on board
  plate.appendChild(h("span", {
    position: "absolute", right: "4px", bottom: "2px",
    fontSize: `${cell * 0.32}px`, color: "var(--text-3)", fontFamily: "var(--font-mono)",
    fontWeight: "700", opacity: "0.7",
  }, "h1"));
  plate.appendChild(h("span", {
    position: "absolute", left: "4px", top: "2px",
    fontSize: `${cell * 0.32}px`, color: "var(--text-3)", fontFamily: "var(--font-mono)",
    fontWeight: "700", opacity: "0.6",
  }, "a8"));

  // WHITE stash — column LEFT of a-file, going DOWN (WQ at slot 0 = a8 row)
  [
    { g: "\u2655", slot: 0, c: accent,          glow: true,  s: 0.95 },
    { g: "\u2659", slot: 1, c: "var(--text-1)", glow: false, s: 0.78 },
    { g: "\u2657", slot: 2, c: "var(--text-1)", glow: false, s: 0.88 },
    { g: "\u2658", slot: 3, c: "var(--text-1)", glow: false, s: 0.90 },
    { g: "\u2656", slot: 4, c: "var(--text-1)", glow: false, s: 0.92 },
  ].forEach(p => {
    plate.appendChild(h("span", {
      position: "absolute",
      left: `${-cell}px`, top: `${cell * p.slot}px`,
      width: `${cell}px`, height: `${cell}px`,
      display: "grid", placeItems: "center",
      fontSize: `${cell * p.s}px`, color: p.c, lineHeight: "1",
      textShadow: p.glow
        ? "0 4px 12px rgba(127,209,168,0.7), 0 0 10px rgba(127,209,168,0.6)"
        : "0 4px 10px rgba(0,0,0,0.6)",
      animation: p.glow ? "cp-cell-float 2.4s ease-in-out 1.2s infinite" : "",
    }, p.g));
  });

  // BLACK stash — column RIGHT of h-file, going UP (BQ at slot 0 = h1 row)
  [
    { g: "\u265b", slot: 0, c: accent,          glow: true,  s: 0.95 },
    { g: "\u265f", slot: 1, c: "var(--text-3)", glow: false, s: 0.78 },
    { g: "\u265d", slot: 2, c: "var(--text-3)", glow: false, s: 0.88 },
    { g: "\u265e", slot: 3, c: "var(--text-3)", glow: false, s: 0.90 },
    { g: "\u265c", slot: 4, c: "var(--text-3)", glow: false, s: 0.92 },
  ].forEach(p => {
    plate.appendChild(h("span", {
      position: "absolute",
      right: `${-cell}px`, bottom: `${cell * p.slot}px`,
      width: `${cell}px`, height: `${cell}px`,
      display: "grid", placeItems: "center",
      fontSize: `${cell * p.s}px`, color: p.c, lineHeight: "1",
      textShadow: p.glow
        ? "0 4px 12px rgba(127,209,168,0.7), 0 0 10px rgba(127,209,168,0.6)"
        : "0 4px 6px rgba(0,0,0,0.4)",
      animation: p.glow ? "cp-cell-float 2.4s ease-in-out infinite" : "",
    }, p.g));
  });

  wrap.appendChild(plate);

  // Corner chip
  wrap.appendChild(h("div", {
    position: "absolute", top: "12px", right: "12px",
    padding: "4px 10px",
    background: "rgba(127,209,168,0.14)",
    border: `1px solid ${accent}`,
    color: accent, fontFamily: "var(--font-mono)", fontSize: "9px",
    letterSpacing: "0.18em", textTransform: "uppercase", fontWeight: "600",
  }, "\u25cf first capture"));

  return wrap;
}

function getDiorama(scenario: Scenario, size: number, color: string): HTMLElement | null {
  switch (scenario) {
    case "welcome":       return dioramaWelcome(size, color);
    case "first-capture": return dioramaFirstCapture(size);
    case "bad-state":     return dioramaBadState(size, color);
    case "won":           return dioramaWon(size, color);
    case "lost":          return dioramaLost(size, color);
    case "draw":          return dioramaDraw(size, color);
    case "long-pause":    return dioramaPause(size, color);
    default:              return null;
  }
}

// ── Confetti ───────────────────────────────────────────────────────────────

function buildConfetti(): HTMLElement {
  const glyphs = ["\u2654", "\u2655", "\u2656", "\u2657", "\u2658", "\u2659"];
  const wrap = h("div", {
    position: "absolute", inset: "0",
    pointerEvents: "none", overflow: "hidden", zIndex: "101",
  });
  for (let i = 0; i < 36; i++) {
    const g = glyphs[i % glyphs.length];
    const left = Math.random() * 100;
    const delay = Math.random() * 1.4;
    const dur = 2.2 + Math.random() * 2;
    const rot = (Math.random() - 0.5) * 720;
    const size = 18 + Math.random() * 22;
    const color = Math.random() > 0.5 ? "#f0ebe0" : "var(--accent)";
    const span = h("span", {
      position: "absolute", top: "-40px",
      left: `${left}%`,
      fontSize: `${size}px`, color, opacity: "0.85",
      animation: `cp-fall ${dur}s linear ${delay}s forwards`,
      transform: `rotate(${rot}deg)`,
      textShadow: "0 1px 2px rgba(0,0,0,0.4)",
    }, g);
    wrap.appendChild(span);
  }
  return wrap;
}

// ── Bad-state diagram ──────────────────────────────────────────────────────

function buildStartPosDiagram(): HTMLElement {
  const size = 140;
  const sq = size / 8;
  const start = ["rnbqkbnr", "pppppppp", "........", "........", "........", "........", "PPPPPPPP", "RNBQKBNR"];
  const GLYPH: Record<string, string> = {
    K: "\u2654", Q: "\u2655", R: "\u2656", B: "\u2657", N: "\u2658", P: "\u2659",
    k: "\u265a", q: "\u265b", r: "\u265c", b: "\u265d", n: "\u265e", p: "\u265f",
  };
  const grid = h("div", {
    width: `${size + sq * 2}px`, height: `${size}px`,
    display: "grid",
    gridTemplateColumns: `${sq}px repeat(8, 1fr) ${sq}px`,
    gridTemplateRows: "repeat(8, 1fr)",
    border: "1px solid var(--line-2)",
    flexShrink: "0",
  });
  start.forEach((row, r) => {
    // Left graveyard column — WQ (white spare queen) at rank 8 (r === 0 = top row)
    grid.appendChild(h("div", {
      background: "var(--bg-1)",
      borderRight: "1px solid var(--line-2)",
      display: "grid", placeItems: "center",
      fontSize: `${sq * 0.7}px`,
      color: "var(--text-1)",
    }, r === 0 ? "\u2655" : ""));

    [...row].forEach((ch, c) => {
      const light = (r + c) % 2 === 0;
      const cell = h("div", {
        background: light ? "var(--board-light)" : "var(--board-dark)",
        display: "grid", placeItems: "center",
        fontSize: `${sq * 0.7}px`,
        color: ch === ch.toUpperCase() && ch !== "." ? "#f0ebe0" : "#1a1a1a",
      }, ch !== "." ? GLYPH[ch] ?? "" : "");
      grid.appendChild(cell);
    });

    // Right graveyard column — BQ (black spare queen) at rank 1 (r === 7 = bottom row)
    grid.appendChild(h("div", {
      background: "var(--bg-1)",
      borderLeft: "1px solid var(--line-2)",
      display: "grid", placeItems: "center",
      fontSize: `${sq * 0.7}px`,
      color: "var(--text-2)",
    }, r === 7 ? "\u265b" : ""));
  });
  const box = h("div", {
    display: "flex", alignItems: "center", gap: "20px",
    padding: "16px",
    background: "var(--bg-2)",
    border: "1px solid var(--line-1)",
    marginTop: "20px",
  });
  const desc = h("div", { flex: "1", minWidth: "0" });
  const label = h("div", {
    fontFamily: "var(--font-mono)", fontSize: "10px",
    letterSpacing: "0.16em", textTransform: "uppercase",
    color: "var(--text-3)", marginBottom: "8px",
  }, "Reset to this position");
  const body = h("div", { fontSize: "13px", color: "var(--text-2)", lineHeight: "1.5" },
    "Place all 32 pieces on their starting squares \u2014 white on ranks 1\u20132, black on 7\u20138.");
  const bodyPromo = h("div", { fontSize: "13px", color: "var(--text-2)", lineHeight: "1.5", marginTop: "6px" },
    "Spare promotion queens go in the shaded corners: \u2655 near a8, \u265b near h1.");
  desc.appendChild(label);
  desc.appendChild(body);
  desc.appendChild(bodyPromo);
  box.appendChild(grid);
  box.appendChild(desc);
  return box;
}

// ── Action buttons ─────────────────────────────────────────────────────────

function buildActions(scenario: Scenario, onDismissOrMinimize: () => void): HTMLElement | null {
  const row = h("div", { display: "flex", gap: "10px", flexWrap: "wrap", marginTop: "22px", pointerEvents: "auto" });
  let added = 0;

  const addBtn = (label: string, primary: boolean, onClick: () => void) => {
    const btn = document.createElement("button");
    btn.className = primary ? "btn btn-accent" : "btn btn-ghost";
    btn.style.cssText = "height:40px;font-size:14px;padding:0 20px;cursor:pointer;";
    btn.textContent = label;
    btn.addEventListener("click", onClick);
    row.appendChild(btn);
    added++;
  };

  switch (scenario) {
    case "first-capture":
      addBtn("Got it", true, onDismissOrMinimize);
      break;
    case "bad-state":
      addBtn("Wipe state", true, () => { cbs?.onWipe(); onDismissOrMinimize(); });
      addBtn("Not now", false, onDismissOrMinimize);
      break;
    case "won":
    case "lost":
    case "draw":
      addBtn("Reset board", true, () => cbs?.onReset());
      addBtn("Close", false, onDismissOrMinimize);
      break;
    case "long-pause":
      addBtn("I\u2019m back", true, onDismissOrMinimize);
      break;
    default:
      break;
  }

  return added > 0 ? row : null;
}

// ── Welcome AUTO control ───────────────────────────────────────────────────

function buildWelcomeAutoControl(): HTMLElement {
  const wrap = h("div", { display: "flex", alignItems: "center", gap: "18px", marginTop: "24px", flexWrap: "wrap", pointerEvents: "auto" });

  const btn = document.createElement("button");
  btn.style.cssText = `
    display:inline-flex;align-items:center;gap:12px;
    padding:0 20px;height:52px;
    background:var(--accent);color:#0e0f10;
    border:none;cursor:pointer;
    font-family:var(--font-display);font-size:18px;font-weight:600;
    letter-spacing:0.04em;transition:background 200ms;
  `;

  const box = document.createElement("span");
  box.className = "auto-cb-box";
  box.style.cssText = `
    display:inline-grid;place-items:center;
    width:22px;height:22px;
    border:2px solid #0e0f10;
    color:var(--accent);font-family:var(--font-mono);
    font-size:16px;font-weight:700;line-height:1;
  `;

  const updateBtn = () => {
    box.style.background = welcomeAutoOn ? "#0e0f10" : "transparent";
    box.textContent = welcomeAutoOn ? "\u2713" : "";
  };
  updateBtn();
  btn.appendChild(box);
  btn.appendChild(document.createTextNode("Turn AUTO on"));

  btn.addEventListener("click", () => {
    const next = !welcomeAutoOn;
    welcomeAutoOn = next;
    updateBtn();
    const msg = wrap.querySelector(".auto-ready-msg") as HTMLElement | null;
    if (msg) {
      msg.style.display = next ? "flex" : "none";
      if (next) msg.style.animation = "cp-fade-in 220ms ease-out";
    }
    void (next ? cbs?.onAutoEnable() : cbs?.onAutoDisable());
  });
  wrap.appendChild(btn);

  const msg = h("span", {
    display: welcomeAutoOn ? "flex" : "none",
    fontFamily: "var(--font-display)", fontSize: "18px", fontWeight: "500",
    color: "var(--accent)", lineHeight: "1.3", maxWidth: "320px",
  }, "Now \u2014 make a move!");
  msg.className = "auto-ready-msg";
  wrap.appendChild(msg);

  return wrap;
}

// ── Takeover modal ─────────────────────────────────────────────────────────

function buildTakeover(scenario: Scenario): HTMLElement {
  const copy = activeCopy ?? pickCopy(scenario);
  const mood = MOOD[scenario];
  const color = moodColor(mood);
  const isWelcome = scenario === "welcome";

  const backdrop = h("div", {
    position: "fixed", inset: "0", zIndex: "100",
    background: "rgba(14,15,16,0.82)",
    backdropFilter: "blur(3px)",
    display: "grid", placeItems: "center",
    animation: "cp-fade-in 220ms ease-out",
    pointerEvents: "auto",
  });

  const handleDismiss = () => {
    if (scenario === "bad-state") {
      badStateMinimized = true;
      activeScenario = null;
    } else if (scenario === "won" || scenario === "lost" || scenario === "draw") {
      gameEndDismissed = true;
      activeScenario = null;
    } else {
      if (scenario === "welcome") doWelcomeDismiss(false);
      else activeScenario = null;
    }
    render();
  };

  backdrop.addEventListener("click", (e) => {
    if (e.target === backdrop) handleDismiss();
  });

  // Card
  const moodGradientColor = mood === "warn" ? "rgba(224,178,96,0.12)" : mood === "sad" ? "rgba(122,124,128,0.10)" : "rgba(127,209,168,0.12)";
  const card = h("div", {
    position: "relative",
    width: isWelcome ? "min(1080px, 94%)" : "min(960px, 92%)",
    maxHeight: "92vh",
    display: "grid",
    gridTemplateColumns: "0.85fr 1fr",
    background: "var(--bg-1)",
    border: "1px solid var(--line-2)",
    boxShadow: "0 32px 100px rgba(0,0,0,0.8)",
    overflow: "hidden",
    animation: "cp-takeover-in 360ms cubic-bezier(0.34, 1.4, 0.64, 1)",
  });

  // Dismiss X
  card.appendChild(dismissX(handleDismiss));

  // Confetti (won)
  if (scenario === "won") card.appendChild(buildConfetti());

  // Left — diorama panel
  const leftPanel = h("div", {
    background: `linear-gradient(135deg, ${moodGradientColor} 0%, var(--bg-0) 100%)`,
    borderRight: "1px solid var(--line-1)",
    display: "grid", placeItems: "center",
    position: "relative",
    minHeight: "460px",
    overflow: "hidden",
  });

  // Faint board-tile pattern
  const pattern = h("div", {
    position: "absolute", inset: "0", opacity: "0.05",
    backgroundImage: "linear-gradient(45deg, var(--text-1) 25%, transparent 25%, transparent 75%, var(--text-1) 75%), linear-gradient(45deg, var(--text-1) 25%, transparent 25%, transparent 75%, var(--text-1) 75%)",
    backgroundSize: "48px 48px",
    backgroundPosition: "0 0, 24px 24px",
  });
  leftPanel.appendChild(pattern);

  const diorama = getDiorama(scenario, 300, color);
  if (diorama) leftPanel.appendChild(diorama);

  // Scenario label
  leftPanel.appendChild(h("div", {
    position: "absolute", bottom: "16px", left: "20px",
    fontFamily: "var(--font-mono)", fontSize: "10px",
    color: "var(--text-3)", letterSpacing: "0.18em", textTransform: "uppercase",
  }, `Garry \u00b7 ${scenario}`));

  card.appendChild(leftPanel);

  // Right — copy panel
  const rightPanel = h("div", {
    padding: "40px 44px",
    display: "flex", flexDirection: "column", justifyContent: "center",
    minHeight: "0", overflowY: "auto",
  });

  rightPanel.appendChild(h("div", {
    fontFamily: "var(--font-display)", fontSize: "12px", fontWeight: "600",
    letterSpacing: "0.22em", textTransform: "uppercase",
    color, marginBottom: "12px",
  }, copy.eyebrow));

  rightPanel.appendChild(h("div", {
    fontFamily: "var(--font-display)",
    fontSize: isWelcome ? "44px" : "40px",
    fontWeight: "600",
    letterSpacing: "-0.025em", color: "var(--text-1)", lineHeight: "1.0",
    marginBottom: "20px",
  }, copy.title));

  if (copy.bullets) {
    const ul = h("ul", { listStyle: "none", padding: "0", margin: "0", display: "flex", flexDirection: "column", gap: "14px" });
    copy.bullets.forEach(b => {
      const li = h("li", {
        display: "flex", alignItems: "baseline", gap: "14px",
        fontFamily: "var(--font-display)", fontSize: "20px", fontWeight: "500",
        color: "var(--text-1)", lineHeight: "1.25", letterSpacing: "-0.005em",
      });
      const dot = h("span", {
        width: "8px", height: "8px", flexShrink: "0",
        background: color, display: "inline-block",
        transform: "translateY(-3px)",
      });
      li.appendChild(dot);
      li.appendChild(document.createTextNode(b));
      ul.appendChild(li);
    });
    rightPanel.appendChild(ul);
  } else if (copy.body) {
    rightPanel.appendChild(h("div", {
      fontSize: isWelcome ? "18px" : "16px",
      lineHeight: "1.55", color: "var(--text-2)",
      maxWidth: "460px",
    }, copy.body));
  }

  if (scenario === "bad-state") rightPanel.appendChild(buildStartPosDiagram());
  if (scenario === "welcome")   rightPanel.appendChild(buildWelcomeAutoControl());

  const actions = buildActions(scenario, handleDismiss);
  if (actions) rightPanel.appendChild(actions);

  card.appendChild(rightPanel);
  backdrop.appendChild(card);
  return backdrop;
}

// ── Speech bubble (first-move) ─────────────────────────────────────────────

function buildBubble(scenario: Scenario): HTMLElement {
  const copy = activeCopy ?? pickCopy(scenario);
  const mood = MOOD[scenario];
  const color = moodColor(mood);

  const handleDismiss = () => {
    activeScenario = null;
    if (scenario === "first-move") firstMoveBubbleDone = true;
    if (firstMoveAutoDismissTimer) { clearTimeout(firstMoveAutoDismissTimer); firstMoveAutoDismissTimer = null; }
    if (scenario === "in-check") {
      if (inCheckAutoDismissTimer) { clearTimeout(inCheckAutoDismissTimer); inCheckAutoDismissTimer = null; }
      inCheckActive = false;
    }
    render();
  };

  const wrap = h("div", {
    position: "fixed", top: "60px", left: "14px", zIndex: "101",
    width: "340px", maxWidth: "calc(100% - 28px)",
    animation: "cp-bubble-in 280ms cubic-bezier(0.34, 1.4, 0.64, 1)",
    pointerEvents: "auto",
  });

  // Caret triangle
  const caret = h("div", {
    position: "absolute", top: "-8px", left: "16px",
    width: "0", height: "0",
    borderLeft: "8px solid transparent",
    borderRight: "8px solid transparent",
    borderBottom: `8px solid ${color}`,
  });
  wrap.appendChild(caret);

  const card = h("div", {
    position: "relative",
    background: "var(--bg-1)",
    border: "1px solid var(--line-2)",
    borderTop: `2px solid ${color}`,
    padding: "14px 16px",
    boxShadow: "0 16px 40px rgba(0,0,0,0.5)",
  });
  card.appendChild(dismissX(handleDismiss));

  const row = h("div", { display: "flex", gap: "10px", alignItems: "flex-start" });

  // Mini knight avatar
  const avatar = h("div", {
    width: "28px", height: "28px",
    display: "grid", placeItems: "center",
    background: color,
    color: "#0e0f10",
    fontFamily: "var(--font-display)", fontWeight: "700",
    fontSize: "20px", lineHeight: "1",
    flexShrink: "0",
    animation: "cp-knight-bounce 600ms cubic-bezier(0.34, 1.56, 0.64, 1)",
  }, "\u265e");
  row.appendChild(avatar);

  const textCol = h("div", { flex: "1", minWidth: "0" });
  textCol.appendChild(h("div", {
    fontFamily: "var(--font-display)", fontSize: "9px", fontWeight: "600",
    letterSpacing: "0.18em", textTransform: "uppercase",
    color, marginBottom: "4px",
  }, copy.eyebrow));
  textCol.appendChild(h("div", {
    fontFamily: "var(--font-display)", fontSize: "15px", fontWeight: "600",
    letterSpacing: "-0.005em", color: "var(--text-1)", lineHeight: "1.2",
    marginBottom: "6px",
  }, copy.title));
  if (copy.body) {
    textCol.appendChild(h("div", {
      fontSize: "12px", lineHeight: "1.5", color: "var(--text-2)",
    }, copy.body));
  }
  row.appendChild(textCol);
  card.appendChild(row);
  wrap.appendChild(card);
  return wrap;
}

// ── Persistent indicators ──────────────────────────────────────────────────

function buildWelcomeChip(): HTMLElement {
  const chip = document.createElement("button");
  chip.style.cssText = `
    position:fixed;bottom:60px;left:20px;z-index:99;
    display:inline-flex;align-items:center;gap:10px;
    padding:8px 14px;
    background:var(--bg-1);
    border:1px solid var(--accent);
    color:var(--accent);
    font-family:var(--font-mono);font-size:10px;
    letter-spacing:0.16em;text-transform:uppercase;
    cursor:pointer;pointer-events:auto;
  `;
  chip.innerHTML = `<span style="font-size:14px">\u265e</span> Tap to learn how to play`;
  chip.addEventListener("click", () => {
    setActiveScenario("welcome");
    welcomeDismissedCount = Math.max(0, welcomeDismissedCount - 1);
    render();
  });
  return chip;
}

function buildGameOverPill(): HTMLElement {
  const sc = gameEndScenario!;
  const label = sc === "won" ? "won" : sc === "lost" ? "lost" : "draw";
  const pill = h("div", {
    position: "fixed", bottom: "60px", left: "20px", zIndex: "99",
    display: "inline-flex", alignItems: "center", gap: "8px",
    padding: "6px 14px",
    background: "var(--bg-1)",
    border: "1px solid var(--line-2)",
    color: "var(--text-3)",
    fontFamily: "var(--font-mono)", fontSize: "10px",
    letterSpacing: "0.16em", textTransform: "uppercase",
    pointerEvents: "none",
  }, `Game over \u00b7 ${label}`);
  return pill;
}

function buildResetBubble(): HTMLElement {
  const wrap = h("div", {
    position: "fixed", bottom: "56px", left: "50%",
    transform: "translateX(-50%)",
    zIndex: "99", pointerEvents: "none",
    display: "flex", flexDirection: "column", alignItems: "center",
    animation: "cp-fade-in 220ms ease-out",
  });
  const card = h("div", {
    background: "var(--bg-1)",
    border: "1px solid var(--line-2)",
    borderTop: "2px solid var(--accent)",
    padding: "10px 16px",
    boxShadow: "0 8px 24px rgba(0,0,0,0.5)",
    fontSize: "13px", color: "var(--text-2)", lineHeight: "1.5",
    textAlign: "center",
  }, "Ready for another? Hit \u00a0");
  const kbd = h("span", {
    fontFamily: "var(--font-mono)", fontSize: "11px",
    color: "var(--text-1)", background: "var(--bg-3)",
    padding: "1px 6px", border: "1px solid var(--line-2)",
  }, "Reset board");
  card.appendChild(kbd);
  card.appendChild(document.createTextNode("\u00a0 below \u2014 I\u2019ll put the pieces back."));
  // Caret pointing down
  const caret = h("div", {
    width: "0", height: "0",
    borderLeft: "8px solid transparent",
    borderRight: "8px solid transparent",
    borderTop: "8px solid var(--line-2)",
  });
  wrap.appendChild(card);
  wrap.appendChild(caret);
  return wrap;
}

// ── Main render ────────────────────────────────────────────────────────────

function render(): void {
  const root = ensureRoot();
  root.innerHTML = "";

  const scenario = activeScenario;

  // Main overlay
  if (scenario) {
    const showModal = BLOCKING[scenario] && !(scenario === "bad-state" && badStateMinimized);
    const showBubble = !BLOCKING[scenario];
    if (showModal) root.appendChild(buildTakeover(scenario));
    else if (showBubble) root.appendChild(buildBubble(scenario));
  }

  // Persistent indicators (always rendered when conditions met)
  if (welcomeDismissedCount > 0 && extPlyCount === 0 && !extOutcome) root.appendChild(buildWelcomeChip());
  if (gameEndScenario && gameEndDismissed) {
    root.appendChild(buildGameOverPill());
    root.appendChild(buildResetBubble());
  }
}
