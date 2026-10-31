package permission

import (
	"os"
	"strings"
	"testing"
)

// TestSessionFixtureReadOnlyShare replays commands of the shape seen in a real
// feature session (issue #14). The classifier must cover the inspection
// majority without ever treating a mutating command as read-only.
func TestSessionFixtureReadOnlyShare(t *testing.T) {
	data, err := os.ReadFile("testdata/session_bash.txt")
	if err != nil {
		t.Fatal(err)
	}
	mutating := []string{"go build", "go vet", "go test", "gofmt -w", "git add", "git commit", "rm -f", "sed -i", "make ci"}
	total, readOnly := 0, 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		total++
		got := ReadOnlyBash(line)
		if got {
			readOnly++
		}
		for _, m := range mutating {
			if strings.Contains(line, "&& "+m) && got {
				t.Fatalf("mutating command classified read-only: %q", line)
			}
		}
	}
	if share := float64(readOnly) / float64(total); share < 0.6 {
		t.Fatalf("only %.0f%% of fixture commands auto-allowed (%d/%d)", share*100, readOnly, total)
	}
}
