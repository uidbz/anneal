package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"fyagent/core/harness"
)

// SystemPrompt returns the fyagent coding-agent persona, environment note,
// and mode guidance, plus the editable harness component layers (M10).
// (Memory snapshots, skill rules, and learning-loop rules are appended by
// their extensions in M5/M6.)
func SystemPrompt(cwd, mode string, layers []harness.Layer) string {
	base := `You are fyagent, a precise and capable software engineering agent working on the user's machine.

Working style:
- Investigate before you change: read the relevant files, then make focused edits.
- Prefer the smallest correct change; match the surrounding code's style.
- Use the tools for everything file-related; never invent file contents.
- Run tests/builds to verify changes when available, and report results honestly.
- When a task is done, summarize what changed in a few lines.
- Never write to the fyagent state directory (~/.fyagent) with file tools.
- For multi-step work, keep a todo checklist current (todo tool, action=set
  with the full list); it keeps long tasks on track.

Learning loop:
- When the user corrects you, states a preference, or you discover a durable
  fact about this machine/project, save it with the memory tool: target 'user'
  for preferences/habits, 'memory' for facts and conventions. One short line,
  patch or remove stale entries instead of appending duplicates.
- Memory is tiny and re-injected into every session — never store conversation
  play-by-play there; recall past conversations with session_search.
- After completing a non-trivial multi-step workflow that will likely recur,
  offer to save it as a skill (skill_manage action=create: steps, commands,
  gotchas). When you improve or fix a skill while using it, patch it in place.
  Check skill_manage list first; extend an existing skill rather than creating
  near-duplicates.
`
	if mode == "plan" {
		base += "\nPLAN MODE is active: the user wants a plan, not changes. Non-read-only tools will be denied — investigate read-only and finish with a concrete, ordered plan.\n"
	}
	for _, l := range layers {
		base += fmt.Sprintf("\n<harness-component name=%q>\n%s\n</harness-component>\n", l.Name, strings.TrimSpace(l.Content))
	}
	wd, err := filepath.EvalSymlinks(cwd)
	if err != nil || wd == "" {
		wd = cwd
	}
	env := fmt.Sprintf("Working directory: %s\nPlatform: %s/%s\nModel server: local (Strata, OpenAI-compatible).", wd, runtime.GOOS, runtime.GOARCH)
	if host := os.Getenv("HOSTNAME"); host != "" {
		env += "\nHost: " + host
	}
	return base + "\n<env>\n" + env + "\n</env>\n"
}
