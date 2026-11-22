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
func PerfLog() string       { return filepath.Join(Root(), "logs", "perf.jsonl") }

// Harness components (M10): editable files mounted into every run.
func Harness() string      { return filepath.Join(Root(), "harness") }
func HarnessLib() string   { return filepath.Join(Root(), "harness-lib") }
func HarnessQueue() string { return filepath.Join(Root(), "harness-queue.json") }
func Improvements() string { return filepath.Join(Root(), "improvements") }
func Overlays() string     { return filepath.Join(Root(), "overlays") }

// Recovery holds tool inputs that were cancelled or never run (a large Write
// interrupted by a stop or time limit), so the work can be re-applied.
func Recovery() string { return filepath.Join(Root(), "recovery") }

// Scheduled tasks: jobs.json is the durable definition store, runs.jsonl the
// append-only result ledger used by notification gateways.
func Schedules() string    { return filepath.Join(Root(), "schedules") }
func ScheduleJobs() string { return filepath.Join(Schedules(), "jobs.json") }
func ScheduleRuns() string { return filepath.Join(Schedules(), "runs.jsonl") }
func ScheduleLock() string { return filepath.Join(Schedules(), "scheduler.lock") }

// GatewayConfig is the anneal-gateway configuration document.
func GatewayConfig() string { return filepath.Join(Root(), "gateway.json") }

// Gentoo holds transient overlays and the audit log for rootless chroot builds.
func Gentoo() string { return filepath.Join(Root(), "gentoo") }

// Ensure creates the standard state directories.
func Ensure() error {
	for _, d := range []string{Root(), Sessions(), Memories(), Skills(), SkillsArchive(), Pending(), Logs(), Harness(), HarnessLib(), Overlays(), Recovery(), Schedules(), Gentoo()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
