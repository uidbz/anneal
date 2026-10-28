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
