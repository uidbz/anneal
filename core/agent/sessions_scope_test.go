package agent

import (
	"context"
	"testing"
	"time"

	"github.com/uidbz/anneal/core/paths"

	"github.com/deepnoodle-ai/dive/session"
)

// TestInitStoreResumesProjectSession: one state dir may hold several
// projects' sessions; boot must resume the newest one belonging to this
// instance's working directory, not the newest overall.
func TestInitStoreResumesProjectSession(t *testing.T) {
	t.Setenv("ANNEAL_HOME", t.TempDir())
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	store, err := session.NewFileStore(paths.Sessions())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const projA, projB = "/proj/a", "/proj/b"
	mk := func(id, cwd, title string) {
		sess, err := store.Open(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		sess.SetMetadata("cwd", cwd)
		sess.SetTitle(title)
		if err := store.Put(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	mk("20260101-000001-aaaaaa", projA, "A session")
	time.Sleep(10 * time.Millisecond)
	mk("20260102-000002-bbbbbb", projB, "B session")

	r := &Runner{cwd: projA}
	if err := r.initStore(); err != nil {
		t.Fatal(err)
	}
	if r.sessID != "20260101-000001-aaaaaa" {
		t.Fatalf("resumed %q, want project A's session", r.sessID)
	}

	// A new session records its project too.
	r.NewSession()
	if r.sess == nil || r.sess.Metadata()["cwd"] != projA {
		t.Fatalf("new session cwd metadata = %v", r.sess.Metadata())
	}
}

// TestInitStoreLegacyFallback: sessions created before cwd tracking still
// resume when nothing better exists (upgrade must not look like data loss).
func TestInitStoreLegacyFallback(t *testing.T) {
	t.Setenv("ANNEAL_HOME", t.TempDir())
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	store, err := session.NewFileStore(paths.Sessions())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Open(context.Background(), "20260101-000001-cccccc")
	if err != nil {
		t.Fatal(err)
	}
	sess.SetTitle("legacy")
	if err := store.Put(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	r := &Runner{cwd: "/proj/new"}
	if err := r.initStore(); err != nil {
		t.Fatal(err)
	}
	if r.sessID != "20260101-000001-cccccc" {
		t.Fatalf("legacy fallback resumed %q", r.sessID)
	}
	if got := r.sess.Metadata()["cwd"]; got != "/proj/new" {
		t.Fatalf("legacy session was not tagged with cwd: %v", got)
	}
}
