# Desktop shell redesign (professional layout)

Status: in progress. One pass, split into phases; every phase leaves the app
building with `go test ./...` green and lands as its own commit.
`-script-ui`, `anneal-headless`, and `anneald` are not touched.

Decisions (agreed with the user):

- All phases in one pass.
- Keep a few status markers (`… ✔ ✖ ⚠ ●`, todo `☐ ▶ ☑ ✖`); drop the decorative
  emoji (🔧 🧠 🪧 💾 🗜 ⚙ 🔐 📋 🛠 ⚡).
- Settings become a real form; the raw JSON editor stays as "Edit JSON…".
- Light and dark themes both supported (`ui.theme`: system | light | dark).
- Ctrl+B toggles the sessions sidebar (it used to open a dialog).
- Sidebar/inspector open state and widths are persisted in `ui.*`.

## Target layout

```
┌──┬──────────────┬───────────────────────────────────┬────────────┐
│◉ │ Sessions  [+]│  Session title  BUILD  [improve][⋯]│ Tasks      │
│▤ │ ▸ Today      │  ──────────────────────────────── │ ☑ step one │
│⚙ │   fix parser │      You                          │ ▶ step two │
│  │   add tests  │      ┌──────────────────────┐     │            │
│  │ ▸ Yesterday  │      │ user message         │     │ Context    │
│  │   …          │      └──────────────────────┘     │ ▇▇▇░ 42%   │
│  │              │      Anneal                       │            │
│  │              │      assistant prose …            │ Session    │
│  │              │      ✔ Read  core/app.go   0.2s   │            │
│  │              │  [approval cards docked here]     │            │
│  │              │  ┌──────────────────────────────┐ │            │
│  │              │  │ multi-line composer          │ │            │
│  │              │  │ [Plan|Build]  model    [Send]│ │            │
│  │              │  └──────────────────────────────┘ │            │
├──┴──────────────┴───────────────────────────────────┴────────────┤
│ ● Ready      model · 262k ctx │ index │ MCP 2/2 · 14 tools │ ↑↓  │
└──────────────────────────────────────────────────────────────────┘
```

## Phase 0 — baseline and file split (no visible change)

- [x] Baseline captures of the current UI (headless Fyne test-driver renders,
      `ANNEAL_UI_CAPTURE=dir go test ./cmd/anneal -run TestUICapture`).
- [x] Split `cmd/anneal/main.go` by concern: `main.go` (flags/boot),
      `shell.go`, `events.go`, `transcript.go`, `approvals.go`, `activity.go`,
      `dialogs.go`, `slash.go`. Behaviour unchanged, tests unchanged.
- [x] Pane execution test: run the embedded `harness.risor` in a test app
      against the real (temp-home) harness API; require
      `HARNESS-PANE-READY`, no `ERROR` status, and rendered content.

## Phase 1 — design tokens and light/dark theme

- Layered palette (dark / light):

  | token | dark | light |
  |---|---|---|
  | bg | `#17181b` | `#ffffff` |
  | rail | `#111214` | `#eef0f2` |
  | sidebar | `#1c1d21` | `#f7f8f9` |
  | raised | `#232429` | `#f3f4f6` |
  | code | `#141518` | `#f6f8fa` |
  | border | `#2e3036` | `#e2e4e8` |
  | fg | `#e6e7ea` | `#1f2328` |
  | muted | `#9a9ca3` | `#5f6670` |
  | accent | `#2dd4bf` | `#0f766e` |
  | ok / warn / err | `#4ade80` / `#f59e0b` / `#ef4444` | `#15803d` / `#b45309` / `#b91c1c` |
  | warnWash | warn @ 10% | warn @ 8% |

- Standard Fyne color names map onto the palette; Anneal names
  (`anneal.rail`, `anneal.sidebar`, `anneal.raised`, `anneal.code`,
  `anneal.border`, `anneal.ok`, `anneal.warn`, `anneal.err`, `anneal.idle`,
  `anneal.warnWash`, …) resolve through the same `Color(name, variant)`.
- `ui.theme` = `system` (default) | `dark` | `light`; a forced variant ignores
  the variant Fyne passes in. Wrapper order `textScaleTheme{brandTheme}` so
  the scale never compounds. `ANNEAL_THEME` env override.
- Theme-aware drawing helpers (`surface.go`): `surface` (fill/border/radius/
  padding, colors re-read on every refresh), `themedText` (compact chrome
  text), `divider`, `statusDot` by token name, `meter`, layouts
  (`padLayout`, `rowLayout`, `maxWidthLayout`). No hard-coded `color.NRGBA`
  outside the palette table.
- Tests: every custom name resolves in both variants and differs; WCAG
  contrast (fg/bg ≥ 7, muted/bg ≥ 4.5, accent/bg ≥ 3) in both variants;
  forced variant wins; text scale does not compound; unknown `ui.theme`
  normalizes to `system`.

## Phase 2 — app frame

- [x] `root = Border(bottom: statusBar, left: rail, center: viewStack)`.
- [x] Views: Chat, Harness, Activity, Settings — a stack driven by
  `nav.show(view)` (replaces `AppTabs`).
- [x] Chat view = sessions sidebar | chat column | inspector, with draggable
  handles (widths in px, clamped 180–480, persisted). Narrow windows hide the
  inspector below 1180 px and the sidebar below 900 px without changing the
  saved preference.
- [x] Activity rail (56 px): logo, Chat / Harness / Activity, Settings pinned at
  the bottom; icon + caption, accent bar + raised background on the active
  item.
- [x] View headers (44 px): chat shows the session title + mode badge and
  actions (sidebar toggle, Improve, inspector toggle, overflow menu: New
  session, Rebuild index, Learn now, Memory…, Skills…, Journey…, Reload
  harness pane).
- [x] Native main menu (File / View / Session / Help) owns the shortcuts
  (menu shortcuts fire regardless of focus). Default window 1280×800.
- [x] Config: `ui.sidebar_open`, `ui.inspector_open`, `ui.sidebar_width`,
  `ui.inspector_width`.
- [x] Tests: nav switches views + rail state; toggles hide/show and persist;
  narrow-window collapse; slash commands that used `tabs.SelectIndex`.

## Phase 3 — sessions sidebar

- [x] Header "Sessions" + new-session button; filter entry.
- [x] `widget.List` of flattened rows: group headings (Today / Yesterday /
  Previous 7 days / Older) and two-line session rows (title or "Untitled
  session", muted relative time). Current session highlighted with an accent
  bar. Right-click: copy session ID.
- [x] Refresh on `session.started`, `session.opened`, `run.finished`.
- [x] Tests: grouping with a fixed clock, filter, current marker, refresh.

## Phase 4 — transcript

- [x] Centered column capped at `820 × textScale` px.
- [x] User turns: "You" caption + raised card. Assistant turns: "Anneal" caption
  (once per turn) + prose; code/table segments on a code surface.
- [x] Tool calls: one compact row (status marker, tool name, argument summary,
  elapsed time); `tool.result` updates the row by `id` (replay pairs FIFO);
  click expands the result preview (built lazily).
- [x] Notices: muted system lines with an optional marker (`✔ ⚠ ✖`); errors in
  the error color.
- [x] Improve/review progress updates one live line (`●`) in place, then turns
  into a final notice.
- [x] Empty state: centered welcome (title, hint, shortcuts) instead of the
  "welcome —" notices.
- [x] Block cap / "Show N earlier messages" and stream chunking unchanged.
- [x] Tests: tool row by id, replay pairing, single progress line, welcome,
  captions once per turn; existing cap/stream tests stay green.

## Phase 5 — composer and approval dock

- [x] Composer on a raised surface: multi-line entry (Enter sends,
  Shift+Enter newline, grows 2→8 rows), toolbar with Plan | Build switch,
  model button, hint (hidden when narrow), single Send/Stop primary button.
- [x] Approval dock between transcript and composer (scrolls beyond ~320 px):
  warn-washed cards with accent bar, title, details on a code surface,
  right-aligned buttons (Reject / Always allow / Allow once; Reject /
  Approve). Ctrl+Enter allows the oldest card once and Ctrl+Backspace rejects
  it when the composer is empty.
- [x] Tests: Enter/Shift+Enter, Send/Stop state, dock placement + removal, mode
  switch follows `mode.changed`.

## Phase 6 — inspector and status bar

- [x] Inspector: Tasks (count badge, markers, in-progress bold, "No tasks"),
  Context (meter: warn ≥ 70 %, err ≥ 82 %; tokens; compactions), Session
  (short id + copy, mode, harness base/overlay).
- [x] Status bar (rail surface, caption text): run dot + state + elapsed time
  while running; clickable chips for model (→ Settings · Model), index
  (→ rebuild after confirm), MCP (→ Settings · Integrations); token totals.
- [x] Tests: chip text/color per state, meter thresholds, elapsed ticker
  start/stop.

## Phase 7 — secondary views

- [x] Settings form (Appearance, Model, Agent, Learning, Context,
  Integrations, Permissions): edits a deep copy of the config, Save goes
  through `App.SaveConfig` (same validation), Revert, inline numeric
  validation (Save disabled while invalid), "Edit JSON…" fallback, discard
  confirm when leaving dirty, "changed elsewhere" banner on external
  `config.changed`. Appearance applies (and persists) immediately.
- [x] Activity: header + Refresh; journey rows (timestamp column, kind badge,
  readable text, newest first); improvement-run cards (outcome, task, note,
  overlay, library entry, candidates, collapsible decisions).
- [x] Harness view: Go header (title, root path, Refresh, Improve); the Risor
  pane renders only content (see below).
- [x] Dialogs: shared monospace text dialog with Copy; Help → Commands /
  Keyboard shortcuts tables; About.

### Risor (harness pane) constraints

- [x] The pane draws with the host app theme; it must not draw its own colored
  backgrounds (`canvas.NewRectangle` takes fixed colors and cannot follow a
  theme switch). Styling = `Card`, `Separator`, `Accordion`, label/button
  `Importance` (via `constants.*`), `TextStyle`.
- [x] Risor v2 rules: no loops (recursion with pre-declared slots), closures see
  only earlier definitions, `} else {` on one line, `sprintf` for numbers,
  every JSON key always present and lists `[]` (never `null`), no keyword
  keys.
- [x] No host-window capability (no dialogs/shortcuts) — unchanged.
- [x] Go header Refresh emits `harness.refresh` (added to the pane event
  filter); the script rebuilds unless an editor has unsaved changes (second
  Refresh forces). Improve uses the Go `improveNow()`; `agent.improveStart`
  stays for compatibility.
- [x] Pane layout: Overview card (root, DISABLED badge, components, guardrail
  counts, hint) → Accordion of files (editor, per-file status, Save) →
  Improvement library card (id, situation, Experience / Promote with
  `WarningImportance` confirm) → Reviewer flags card → Experience card →
  notice label. Open accordion items survive rebuilds.
- [x] Gap: MultiLineEntry has no `SetMinRowsVisible` binding — logged in
  `docs/FYNERISOR-GAPS.md`.
- [x] Tests: pane execution test in light and dark; unsaved-changes guard;
  promote two-step; `harness.refresh` passes `paneEvent`; `-check-ui`.

## Phase 8 — polish, docs, verification

- Narrow windows (900 px), text scale 80/100/150/200 %, transcript
  performance (1,000 blocks, long streams), keyboard reachability, contrast.
- Capture test (`ANNEAL_UI_CAPTURE=dir`) renders every view in light and
  dark for visual review.
- Docs: README UI paragraph, `docs/USING.md` (layout, shortcuts, settings),
  `docs/CONFIG.md` (`ui.*`), `progress.md` note.
- Final: `go vet ./...`, `go test ./...`, `-check-ui`, `-check-harness`,
  `-check-script-ui` against a minimal `main.risor`.
