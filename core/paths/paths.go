// Package paths resolves fyagent's per-user state under ~/.fyagent.
package paths

import (
	"os"
	"path/filepath"
)

// Root returns the fyagent state directory (override with FYAGENT_HOME).
func Root() string {
	if h := os.Getenv("FYAGENT_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".fyagent"
	}
	return filepath.Join(home, ".fyagent")
}

func Config() string        { return filepath.Join(Root(), "config.json") }
func Sessions() string      { return filepath.Join(Root(), "sessions") }
func Memories() string      { return filepath.Join(Root(), "memories") }
func Skills() string        { return filepath.Join(Root(), "skills") }
func SkillsArchive() string { return filepath.Join(Root(), "skills-archive") }
func Pending() string       { return filepath.Join(Root(), "pending") }
func Bundled() string       { return filepath.Join(Root(), "bundled") }
func Logs() string          { return filepath.Join(Root(), "logs") }

// Harness components (M10): editable files mounted into every run.
func Harness() string      { return filepath.Join(Root(), "harness") }
func HarnessLib() string   { return filepath.Join(Root(), "harness-lib") }
func HarnessQueue() string { return filepath.Join(Root(), "harness-queue.json") }
func Probes() string       { return filepath.Join(Root(), "probes") }
func Improvements() string { return filepath.Join(Root(), "improvements") }
func Overlays() string     { return filepath.Join(Root(), "overlays") }

// Ensure creates the standard state directories.
func Ensure() error {
	for _, d := range []string{Root(), Sessions(), Memories(), Skills(), SkillsArchive(), Pending(), Logs(), Harness(), HarnessLib(), Overlays()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
