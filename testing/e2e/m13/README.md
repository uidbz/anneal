# m13 — improvement-library live gate (~50 min; two full steps + promote)

Proves harnessing-level recursion end-to-end against live Strata:

1. **run 1** (Sub task): full step → library entry with
   `harness/` snapshot + `EXPERIENCE.md` + `meta.json` + INDEX.md line.
2. **run 2** (Mul task): starts from ℋ₀ again but the Propose/Compose
   prompts carry run 1's experience; its composer must be selective —
   known-good outcome dropped probe-infrastructure noise
   ("preseed files", "probe jail") as not agent-behavior rules.
3. **promote round-trip**: `/harness promote <id>` in the REPL moves the
   entry's components into the GLOBAL `~/.anneal/harness/`, backing up
   the replaced files under `harness-lib/promote-backup/<ts>/`.

```sh
make live-m13-test        # repo root; Strata up; ~50 min
```

Gate output marker: `M13: === M13 LIBRARY OK`.

Known-good outcome (2026-10-25, qwen3.8-flash-next): 2 entries
(sub + mul tasks) with parsed situation lines in INDEX.md; promote
applied with backup; library search visible via tie
(`ANNEAL_LIVE_TIE=1 go test ./core/tieindex -run TestLiveExperience`).

Note: rebuild `bin/anneal-headless` before running — the gate uses the
REPL's `/harness promote` handler (an old binary silently sends the
command to the model instead).
