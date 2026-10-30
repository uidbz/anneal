// Package paths resolves anneal's per-user state under ~/.anneal.
package paths

import (
	"os"
	"path/filepath"
)

// Root returns the anneal state directory (override with ANNEAL_HOME).
func Root() string {
	if h := os.Getenv("ANNEAL_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".anneal"
	}
	return filepath.Join(home, ".anneal")
}

func Config() string        { return filepath.Join(Root(), "config.json") }
func Permissions() string   { return filepath.Join(Root(), "permissions.json") }
func Sessions() string      { return filepath.Join(Root(), "sessions") }
func Memories() string      { return filepath.Join(Root(), "memories") }
func Skills() string        { return filepath.Join(Root(), "skills") }
func SkillsArchive() string { return filepath.Join(Root(), "skills-archive") }
func Pending() string       { return filepath.Join(Root(), "pending") }
func Logs() string          { return filepath.Join(Root(), "logs") }

// Harness components (M10): editable files mounted into every run.
func Harness() string      { return filepath.Join(Root(), "harness") }
func HarnessLib() string   { return filepath.Join(Root(), "harness-lib") }
func HarnessQueue() string { return filepath.Join(Root(), "harness-queue.json") }
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
