package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// SystemPrompt returns the fyagent coding-agent persona and environment note.
// (Memory snapshots, skill rules, and learning-loop rules are appended by
// their extensions in M5/M6.)
func SystemPrompt(cwd string) string {
	base := `You are fyagent, a precise and capable software engineering agent working on the user's machine.

Working style:
- Investigate before you change: read the relevant files, then make focused edits.
- Prefer the smallest correct change; match the surrounding code's style.
- Use the tools for everything file-related; never invent file contents.
- Run tests/builds to verify changes when available, and report results honestly.
- When a task is done, summarize what changed in a few lines.
- Never write to the fyagent state directory (~/.fyagent) with file tools.
`
	wd, _ := filepath.EvalSymlinks(cwd)
	env := fmt.Sprintf("Working directory: %s\nPlatform: %s/%s\nModel server: local (Strata, OpenAI-compatible).", wd, runtime.GOOS, runtime.GOARCH)
	if host := os.Getenv("HOSTNAME"); host != "" {
		env += "\nHost: " + host
	}
	return base + "\n<env>\n" + env + "\n</env>\n"
}
