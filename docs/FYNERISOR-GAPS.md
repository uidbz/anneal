# fynerisor feature gaps (fyagent perspective)

Living tracker of fynerisor capabilities missing for fyagent. When a gap is hit,
add it here and file an issue at <https://github.com/uidbz/fynerisor/issues>.

| # | Gap | Needed for | Status |
|---|-----|-----------|--------|
| 1 | `app.Quit()` / `window.Close()` not bound (README + EXAMPLES promise them; `WIDGET_STATUS.md` lists `window.Close()` as done) | clean quit from scripts | [issue #2](https://github.com/uidbz/fynerisor/issues/2) |
| 2 | `Markdown` text immutable after creation — no `SetText` / writable `.Text` | chat transcript, streaming views, any refreshable markdown | [issue #3](https://github.com/uidbz/fynerisor/issues/3) |
| 3 | Containers (VBox/HBox/Border/Scroll/Grid/Center/Max/Padded/Split/Stack) lack `Append`/`Remove`/`Refresh` — only apptabs has item ops | dynamic views (transcript blocks, session list, approval inbox) without full `SetContent` rebuilds | [issue #4](https://github.com/uidbz/fynerisor/issues/4) |

## Workarounds currently in use (fyagent)

1. `agent.quit()` Go bridge calling `app.Quit()` on the Go side.
2. Transcript = streaming tail in a writable `Label`; finalized blocks are
   immutable `Markdown` widgets; layout recomposed (new containers + `SetContent`)
   on every structural change.
3. Same recompute-everything pattern; fine at chat scale, wasteful for large lists.

## Non-gaps (already available, easy to miss)

- `constants` global exposes Fyne enums: `constants.TextWrapWord/Break/Off`,
  `ImportanceHigh`, `ScrollVerticalOnly`, `TextTruncateEllipsis`, …
  (use these instead of magic ints).
- `window.Do(fn)` for main-thread execution; `guithread.Do` Go-side.
- `require(["v0.9", ...])` version/feature gating.

## Risor v2 notes (not fynerisor bugs — upstream to deepnoodle-ai/risor if ever needed)

- No `map`/`filter`/`reduce` builtins; risor v2 stdlib modules are only `math`,
  `rand`, `regexp`.
- Reserved words (`in`, `len`, …) cannot be used as dot-accessed map keys.
- Closures resolve free variables at compile time (affect late-defined helpers).
