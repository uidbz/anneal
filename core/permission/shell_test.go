package permission

import (
	"path/filepath"
	"testing"
)

func TestScanCommand(t *testing.T) {
	cases := []struct {
		name     string
		command  string
		commands []Command
		dirs     []string
	}{
		{
			name:    "compound",
			command: "git status --porcelain && go test ./...",
			commands: []Command{
				{Resource: "git status --porcelain", Save: "git status *"},
				{Resource: "go test ./...", Save: "go test *"},
			},
		},
		{
			name:     "quoted operator",
			command:  `echo "a;b"`,
			commands: []Command{{Resource: `echo "a;b"`, Save: "echo *"}},
		},
		{
			name:     "cd then list",
			command:  "cd /tmp && ls -la",
			commands: []Command{{Resource: "ls -la", Save: "ls *"}},
			dirs:     []string{"/tmp"},
		},
		{
			name:    "cd only",
			command: "cd /tmp",
			dirs:    []string{"/tmp"},
		},
		{
			name:     "assignment prefix",
			command:  "FOO=bar go test ./...",
			commands: []Command{{Resource: "FOO=bar go test ./...", Save: "go test *"}},
		},
		{
			name:     "substitution stays whole",
			command:  "rm -rf $(pwd)",
			commands: []Command{{Resource: "rm -rf $(pwd)", Save: "rm *"}},
		},
		{
			name:     "git config prefix",
			command:  "git config user.name johan",
			commands: []Command{{Resource: "git config user.name johan", Save: "git config user.name *"}},
		},
		{
			// Regression: the `&` in `2>&1` used to split the command and
			// produced a bogus "1" segment that needed its own approval.
			name:    "stderr redirect then pipe",
			command: "go test ./... 2>&1 | tail -20",
			commands: []Command{
				{Resource: "go test ./... 2>&1", Save: "go test *"},
				{Resource: "tail -20", Save: "tail *"},
			},
		},
		{
			name:     "and-redirect",
			command:  "make ci &> build.log",
			commands: []Command{{Resource: "make ci &> build.log", Save: "make ci *"}},
		},
		{
			name:     "fd duplication",
			command:  "echo hi >&2",
			commands: []Command{{Resource: "echo hi >&2", Save: "echo *"}},
		},
		{
			name:    "pipe stderr",
			command: "make 2>&1 |& tee log",
			commands: []Command{
				{Resource: "make 2>&1", Save: "make *"},
				{Resource: "tee log", Save: "tee *"},
			},
		},
		{
			name:     "background job still splits",
			command:  "sleep 1 & echo done",
			commands: []Command{{Resource: "sleep 1", Save: "sleep *"}, {Resource: "echo done", Save: "echo *"}},
		},
		{
			// A substituted cd stays one exact resource: its partial tokens
			// must not become directory or "cd *" grants.
			name:     "substitution cd is exact",
			command:  `cd $(go env GOMODCACHE)/github.com/x && ls`,
			commands: []Command{{Resource: `cd $(go env GOMODCACHE)/github.com/x && ls`, Save: `cd $(go env GOMODCACHE)/github.com/x && ls`}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ScanCommand(c.command)
			if len(got.Commands) != len(c.commands) {
				t.Fatalf("commands = %+v, want %+v", got.Commands, c.commands)
			}
			for i := range c.commands {
				if got.Commands[i] != c.commands[i] {
					t.Errorf("command %d = %+v, want %+v", i, got.Commands[i], c.commands[i])
				}
			}
			if len(got.Directories) != len(c.dirs) {
				t.Fatalf("dirs = %v, want %v", got.Directories, c.dirs)
			}
			for i := range c.dirs {
				if got.Directories[i] != c.dirs[i] {
					t.Errorf("dir %d = %q, want %q", i, got.Directories[i], c.dirs[i])
				}
			}
		})
	}
}

func TestResolveDirectory(t *testing.T) {
	if got := ResolveDirectory("/work", "sub/dir"); got != filepath.Clean("/work/sub/dir") {
		t.Fatalf("relative: %q", got)
	}
	if got := ResolveDirectory("/work", "/tmp"); got != filepath.Clean("/tmp") {
		t.Fatalf("absolute: %q", got)
	}
}
