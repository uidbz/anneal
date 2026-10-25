package tieindex

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/uidbz/anneal/core/config"
)

// TestLiveExperience indexes and queries a library EXPERIENCE note on the
// real triplestore. Opt-in: ANNEAL_LIVE_TIE=1.
func TestLiveExperience(t *testing.T) {
	if os.Getenv("ANNEAL_LIVE_TIE") != "1" && os.Getenv("FYAGENT_LIVE_TIE") != "1" {
		t.Skip("set ANNEAL_LIVE_TIE=1 to test against the real triplestore")
	}
	x := New(config.Tie{Enabled: true, Collection: "anneal", ConfigName: ""})
	if err := x.Connect(); err != nil {
		t.Skipf("tie not reachable: %v", err)
	}
	id := "gtest-exp-verify-000000"
	text := "## lessons\n- verification guardrail made the agent re-run go test before declaring done"
	if err := x.IndexExperience(id, text); err != nil {
		t.Fatal(err)
	}
	hits, err := x.SearchExperience("verification guardrail go test", 5)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range hits {
		if h.ID == id {
			found = true
			if !strings.Contains(h.Snippet, "go test") {
				t.Fatalf("snippet: %q", h.Snippet)
			}
		}
	}
	if !found {
		t.Fatalf("entry not found in %d hits", len(hits))
	}
	// cleanup
	tc, err := x.client()
	if err != nil {
		return
	}
	b := tc.NewBatch()
	key := "harness-exp:" + id
	b.Delete(key, "tie-type", "harness-exp")
	b.Delete(key, "snippet", snippet(text, 400))
	for _, w := range tokens(text, 300) {
		b.Delete(key, "tag", w)
	}
	if _, err := tc.BatchContext(context.Background(), b); err != nil {
		t.Logf("cleanup: %v", err)
	}
}
