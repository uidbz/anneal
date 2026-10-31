package permission

import "testing"

func TestReadOnlyBash(t *testing.T) {
	readOnly := []string{
		"ls -la",
		"cat go.mod",
		"grep -rn TODO .",
		"rg -n 'func main' .",
		"sed -n '1,20p' go.mod",
		"sed -n '/error/p' log.txt",
		"git status --porcelain",
		"git diff HEAD -- src",
		"git log --oneline -5",
		"git branch --list",
		"git stash list",
		"git config --get user.name",
		"git remote -v",
		"go env GOMODCACHE",
		"go list ./...",
		"gofmt -l .",
		"find . -name '*.go'",
		"wc -l go.mod",
		"head -30 docs/CONFIG.md",
		"gh issue list --limit 10",
		"FOO=bar git status",
		"ls && pwd",
	}
	for _, cmd := range readOnly {
		if !ReadOnlyBash(cmd) {
			t.Errorf("ReadOnlyBash(%q) = false, want true", cmd)
		}
	}

	mutating := []string{
		"",
		"rm -rf build",
		"make ci",
		"go test ./...",
		"go build ./...",
		"go vet ./...",
		"go env -w GOFLAGS=-mod=mod",
		"go mod download",
		"sed -i 's/a/b/' f",
		"sed -n 's/a/b/w out' f",
		"sed '1e cat /etc/passwd' f",
		"find . -delete",
		"find . -exec rm {} +",
		"rg --pre ./run foo",
		"git branch -D old",
		"git config user.name x",
		"git remote add origin u",
		"git stash pop",
		"gh issue close 1",
		"echo hi > out",
		"cat < in",
		"ls | tee log",
		"curl http://example.com",
		"python3 -c 'print(1)'",
		"cd /tmp",
		"cd $(pwd)",
		"xargs rm",
		"sudo ls",
	}
	for _, cmd := range mutating {
		if ReadOnlyBash(cmd) {
			t.Errorf("ReadOnlyBash(%q) = true, want false", cmd)
		}
	}
}

func TestIsVerifyCommand(t *testing.T) {
	for _, cmd := range []string{"go test ./...", "go build ./...", "make ci", "cargo test", "npm test", "pytest -q"} {
		if !IsVerifyCommand(cmd) {
			t.Errorf("IsVerifyCommand(%q) = false", cmd)
		}
	}
	for _, cmd := range []string{"ls", "go env", "git status", "rm -rf x"} {
		if IsVerifyCommand(cmd) {
			t.Errorf("IsVerifyCommand(%q) = true", cmd)
		}
	}
}
