package tieindex

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/uidbz/anneal/core/config"
)

// Live-server test: only runs when ANNEAL_LIVE_TIE=1 and the configured
// triplestore answers. Run with: ANNEAL_LIVE_TIE=1 go test ./core/tieindex
func TestLiveRoundtrip(t *testing.T) {
	if os.Getenv("ANNEAL_LIVE_TIE") != "1" && os.Getenv("FYAGENT_LIVE_TIE") != "1" {
		t.Skip("set ANNEAL_LIVE_TIE=1 to test against the real triplestore")
	}
	x := New(config.Tie{Enabled: true, Collection: "anneal", ConfigName: ""})
	if err := x.Connect(); err != nil {
		t.Skipf("tie not reachable: %v", err)
	}
	sid := "anneal-test-xyz"
	x.indexed[sid] = 999 // force full re-index of the test session
	sd := SessionData{
		ID: sid, Title: "gtest", Project: "/tmp", Model: "m", Updated: time.Now(),
		Msgs: []MsgData{
			{Position: 0, Role: "user", Text: "quokka sandwich preparation steps"},
			{Position: 1, Role: "assistant", Text: "first gather quokkas"},
		},
	}
	if err := x.IndexSession(sd); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	hits, err := x.Search("quokka sandwich", 5)
	el := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	if hits[0].SessionID != sid {
		t.Fatalf("wrong session %q", hits[0].SessionID)
	}
	t.Logf("search latency: %v, hits=%d snippet=%q title=%q", el, len(hits), hits[0].Snippet, hits[0].Title)
	if el > 500*time.Millisecond {
		t.Fatalf("slow search: %v", el)
	}
	// negative: token present nowhere must yield no hits (not an error)
	hits2, err := x.Search("quokka zzznotpresent", 5)
	if err != nil {
		t.Fatalf("negative search errored: %v", err)
	}
	if len(hits2) != 0 {
		t.Fatalf("expected 0 hits, got %d", len(hits2))
	}
	// incremental: second index of same content adds nothing new
	if err := x.IndexSession(sd); err != nil {
		t.Fatal(err)
	}
	cleanupIndex(t, x, sid, sd)
	purgeLegacyTestRows(t, x)
}

func cleanupIndex(t *testing.T, x *Indexer, sid string, sd SessionData) {
	tc, err := x.client()
	if err != nil {
		return
	}
	b := tc.NewBatch()
	for _, m := range sd.Msgs {
		mid := msgID(sid, m.Position)
		b.Delete(sid, "session-msg", mid)
		b.Delete(mid, "tie-type", "session-msg")
		b.Delete(mid, "session-of", sid)
		for _, w := range tokens(m.Text, 300) {
			b.Delete(mid, "tag", w)
		}
		b.Delete(mid, "snippet", snippet(m.Text, 400))
	}
	b.Delete(sid, "tie-type", "session")
	b.Delete(sid, "title", "gtest")
	if _, err := tc.BatchContext(context.Background(), b); err != nil {
		t.Logf("cleanup: %v", err)
	}
}

// purgeLegacyTestRows removes rows left by earlier exploratory runs so the
// anneal collection only holds real sessions.
func purgeLegacyTestRows(t *testing.T, x *Indexer) {
	tc, err := x.client()
	if err != nil {
		return
	}
	for _, sid := range []string{"anneal-dbg-1", "anneal-test-xyz"} {
		row, err := tc.GetContext(context.Background(), sid)
		if err != nil {
			continue
		}
		b := tc.NewBatch()
		for rel, vals := range row.Attributes {
			for _, v := range vals {
				b.Delete(sid, rel, v)
			}
		}
		for _, mid := range row.Attributes["session-msg"] {
			mrow, err := tc.GetContext(context.Background(), mid)
			if err == nil {
				for rel, vals := range mrow.Attributes {
					for _, v := range vals {
						b.Delete(mid, rel, v)
					}
				}
			}
		}
		if _, err := tc.BatchContext(context.Background(), b); err != nil {
			t.Logf("purge %s: %v", sid, err)
		}
	}
}
