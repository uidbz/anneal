# fynerisor feature gaps (anneal perspective)

Living tracker of fynerisor capabilities missing for anneal. When a gap is hit,
add it here and file an issue at <https://github.com/uidbz/fynerisor/issues>.
Fixes may also be implemented directly (issue first) — the local checkout lives
at `/go/src/github.com/uidbz/fynerisor`, and anneal `go.mod` `replace`s
fynerisor to it until the next release.

| # | Gap | Needed for | Status |
|---|-----|-----------|--------|
| 1 | `app.Quit()` / `window.Close()` not bound (README + EXAMPLES promise them) | clean quit from scripts | **fixed on fynerisor main** (a0280fb, issue #2) — released in next version |
| 2 | `Markdown` text immutable after creation — no `SetText` / writable `.Text` | chat transcript, streaming views | **fixed on fynerisor main** (45e3eca, issue #3) |
| 3 | Containers lacked `Append`/`Remove`/`Refresh`; Border no slot setters; Scroll no SetContent | dynamic views without full `SetContent` rebuilds | **fixed on fynerisor main** (dc84d6b, issue #4) |
| 4 | `tie.connect`: namespace/collection options silently overridden by config-file `[Collections.*]`/`DefaultCollection` (also hijacks the connect URL) | M4 session_search against local tie | **fixed on fynerisor main** (1c0e93f, issue #5) |
| 5 | No synchronous script compile-check — `LoadScript` is fire-and-forget, errors only surface async via stderr from `Execute()` | CI gate for UI scripts (`anneal -check-ui`) | **fixed on fynerisor main** (0b826e9, issue #6) — `Window.CompileScript()` |
| 6 | No way to embed a scripted view inside an existing Go/Fyne layout | hybrid UI: Go shell + scripted harness pane | **fixed on fynerisor main** (b2b1478) — `gui.NewCanvasObject` + `WithHostWindow`; used by `cmd/anneal` |

## Workarounds currently in use (Anneal)

- Hybrid shell: Go/Fyne owns the transcript and window; the harness pane uses
  `gui.NewCanvasObject` and a narrow, pull-based `agent` API. The legacy
  all-Risor UI still uses the recompose pattern.
- `agent.quit()` Go bridge (still fine; `app.Quit()` now also works for
  gui.NewApp apps).

## Non-gaps (already available, easy to miss)

- `constants` global: `constants.TextWrapWord/Break/Off`, `ImportanceHigh`,
  `ScrollVerticalOnly`, `TextTruncateEllipsis`, … (use instead of magic ints).
- `window.Do(fn)` script-side; `guithread.Do` Go-side; all widget/container
  methods already thread-safe via guithread.Do.
- `require(["v0.9", ...])` version/feature gating.
- Scroll `ScrollToBottom/Top` (commit d84c9e4).

## Risor v2 notes (not fynerisor bugs — upstream to deepnoodle-ai/risor if ever needed)

- No `map`/`filter`/`reduce` builtins; risor v2 stdlib modules are only `math`,
  `rand`, `regexp`.
- Reserved words (`in`, `len`, …) cannot be used as dot-accessed map keys.
- Closures resolve free variables at compile time (affect late-defined helpers).
- `string + int` is a type error — use `sprintf` for interpolation.
