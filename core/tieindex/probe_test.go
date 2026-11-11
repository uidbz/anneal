package tieindex

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The tie triplestore mounts its protocol at POST /{request}: the probe
// must use POST against /live, not GET against /-/live (405/404 territory
// that never proves the tie request router is up).
func TestProbeLiveUsesPostLive(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		if r.Method != http.MethodPost {
			// What Go's method-pattern mux answers for GET /live.
			w.Header().Set("Allow", "POST")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/live" {
			io.WriteString(w, `{"Success":true}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	if err := probeLive(srv.URL + "/"); err != nil {
		t.Fatalf("probeLive: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/live" {
		t.Fatalf("probe used %s %s, want POST /live", gotMethod, gotPath)
	}
}

// A live triplestore answers the unknown request id with its 404 JSON
// envelope — still proof of liveness (this is what real tie v0.7.x does).
func TestProbeLiveUnknownRequestStillLive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"Success":false,"Message":"Unknown request: \"live\""}`)
	}))
	defer srv.Close()
	if err := probeLive(srv.URL); err != nil {
		t.Fatalf("probeLive against tie-style 404: %v", err)
	}
}

// Unreachable must degrade, not panic: transport failure is the only
// error probeLive reports.
func TestProbeLiveUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // nobody is listening anymore
	if err := probeLive(srv.URL); err == nil {
		t.Fatal("probeLive reported live against a dead listener")
	}
}
