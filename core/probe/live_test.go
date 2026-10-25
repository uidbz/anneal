package probe

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uidbz/anneal/core/config"
	"github.com/uidbz/anneal/core/model"

	"github.com/deepnoodle-ai/dive/llm"
)

// TestLiveReplay replays a real mutation task against live Strata in a
// sandbox, proving the probe plumbing end-to-end (record written, sandbox
// mutated, original untouched). Opt-in: ANNEAL_LIVE_PROBE=1.
func TestLiveReplay(t *testing.T) {
	if os.Getenv("ANNEAL_LIVE_PROBE") != "1" {
		t.Skip("set ANNEAL_LIVE_PROBE=1 to run against live Strata")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	modelID := cfg.Provider.Model
	if modelID == "" || modelID == "auto" {
		resp, err := http.Get(strings.TrimSuffix(cfg.Provider.Endpoint, "/chat/completions") + "/models")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var doc struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if json.NewDecoder(resp.Body).Decode(&doc) != nil || len(doc.Data) == 0 {
			t.Fatal("cannot resolve model from /v1/models")
		}
		modelID = doc.Data[0].ID
	}

	base := "/tmp/opencode/probe-live"
	os.RemoveAll(base)
	src := filepath.Join(base, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	mainSrc := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte(mainSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Prepare(src, filepath.Join(base, "sb"))
	if err != nil {
		t.Fatal(err)
	}

	system := "You are a probe replaying one coding task. Work ONLY on absolute paths under " +
		s.WS + ". Bash commands must set working_directory inside it. Verify your change by re-reading the file."
	task := "Add the comment line // probe-marker as the first line of " + s.WS +
		"/main.go, then read the file back to confirm."

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	rec, rerr := Run(ctx, Options{
		Name: "reference", Cfg: cfg, ModelID: modelID,
		Model:         func() llm.LLM { return model.NewProvider(cfg.Provider, modelID) },
		System:        system,
		Task:          task,
		Scratch:       s,
		OutDir:        filepath.Join(base, "out"),
		MaxIterations: 20,
		WallTimeoutS:  220,
	})
	if rerr != nil {
		t.Fatalf("probe run: %v", rerr)
	}
	if len(rec.Steps) == 0 || rec.Deliverable == "" {
		t.Fatalf("thin record: steps=%d deliverable=%q", len(rec.Steps), rec.Deliverable)
	}

	// sandbox mutated
	wsb, err := os.ReadFile(filepath.Join(s.WS, "main.go"))
	if err != nil || !strings.Contains(string(wsb), "// probe-marker") {
		t.Fatalf("sandbox not mutated: %q %v", wsb, err)
	}
	// original untouched
	ob, _ := os.ReadFile(filepath.Join(src, "main.go"))
	if string(ob) != mainSrc {
		t.Fatalf("original modified: %q", ob)
	}
	// record on disk + non-trivial trajectory
	rb, err := os.ReadFile(filepath.Join(base, "out", "reference.json"))
	if err != nil {
		t.Fatal(err)
	}
	var back Record
	if json.Unmarshal(rb, &back) != nil || len(back.Steps) == 0 {
		t.Fatalf("record: %s", rb)
	}
	t.Logf("live replay ok: %d steps, %ds, config=%s harness=%s",
		len(back.Steps), int(back.DurationS), back.ConfigHash, back.HarnessHash)
	if d := back.Digest(0); !strings.Contains(d, "task:") {
		t.Fatalf("digest: %q", d)
	}
}
