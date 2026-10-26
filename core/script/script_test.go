package script

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newEnv isolates both sandbox roots: ANNEAL_HOME moves the anneal state
// dir (paths.Root) and workdir is the project stand-in.
func newEnv(t *testing.T) (workdir, annealRoot string) {
	t.Helper()
	annealRoot = filepath.Join(t.TempDir(), "anneal")
	t.Setenv("ANNEAL_HOME", annealRoot)
	workdir = t.TempDir()
	return workdir, annealRoot
}

func run(t *testing.T, wd, src string) (*Result, error) {
	t.Helper()
	return Run(context.Background(), Options{Script: src, Workdir: wd, Timeout: 5 * time.Second})
}

func TestRunValueAndPrint(t *testing.T) {
	wd, _ := newEnv(t)
	res, err := run(t, wd, "print(\"one\")\nprint(\"two\")\nlet m = {\"a\": [1, 2], \"b\": \"x\"}\nm")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Output != "one\ntwo\n" {
		t.Errorf("output = %q, want %q", res.Output, "one\ntwo\n")
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(res.ValueJSON), &got); err != nil {
		t.Fatalf("value json %q: %v", res.ValueJSON, err)
	}
	if got["b"] != "x" {
		t.Errorf("value = %s", res.ValueJSON)
	}
}

func TestCompileErrorHasNoSideEffects(t *testing.T) {
	wd, _ := newEnv(t)
	_, err := run(t, wd, "fs.write(\"marker.txt\", \"boom\")\nlet x =")
	if err == nil || !strings.Contains(err.Error(), "parse error") {
		t.Fatalf("want parse error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(wd, "marker.txt")); statErr == nil {
		t.Fatal("script side effect ran despite compile error")
	}
}

func TestUnavailableModuleRejected(t *testing.T) {
	wd, _ := newEnv(t)
	_, err := run(t, wd, "require([\"@http\"])\nprint(http)")
	if err == nil || !strings.Contains(err.Error(), "@http is not available") {
		t.Fatalf("want module denial, got %v", err)
	}
	if !strings.Contains(err.Error(), "available: csv, filepath, fs, json, strings, time") {
		t.Errorf("denial should list available modules: %v", err)
	}
}

func TestRequestedModuleRejected(t *testing.T) {
	wd, _ := newEnv(t)
	_, err := Run(context.Background(), Options{
		Script: "1", Modules: []string{"exec"}, Workdir: wd, Timeout: time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "@exec is not available") {
		t.Fatalf("want module denial, got %v", err)
	}
}

func TestDeclaredHostModuleAccepted(t *testing.T) {
	wd, _ := newEnv(t)
	res, err := run(t, wd, "require([\"@fs\", \"@json\"])\nfs.exists(\"nope.txt\")")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.ValueJSON != "false" {
		t.Errorf("value = %q, want false", res.ValueJSON)
	}
}

func TestImportRejected(t *testing.T) {
	wd, _ := newEnv(t)
	for _, src := range []string{
		`import("utils.risor")`,
		"let p = \"utils.risor\"\nimport(p)",
	} {
		_, err := run(t, wd, src)
		if err == nil || !strings.Contains(err.Error(), "not available") {
			t.Errorf("import %q: want rejection, got %v", src, err)
		}
	}
}

func TestTimeout(t *testing.T) {
	wd, _ := newEnv(t)
	_, err := Run(context.Background(), Options{
		Script: "range(200000000).each(x => x * x)", Workdir: wd, Timeout: 200 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want timeout, got %v", err)
	}
}

func TestFSReadWriteRemoveInWorkdir(t *testing.T) {
	wd, _ := newEnv(t)
	res, err := run(t, wd, `[fs.write("out/report.txt", "hello"), fs.read("out/report.txt")]`)
	if err != nil {
		t.Fatalf("run: %v (%s)", err, res.Output)
	}
	if res.ValueJSON != `[true,"hello"]` {
		t.Errorf("value = %s", res.ValueJSON)
	}
	res, err = run(t, wd, `fs.remove("out/report.txt")`)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	res, err = run(t, wd, `fs.exists("out/report.txt")`)
	if err != nil || res.ValueJSON != "false" {
		t.Fatalf("exists after remove: %v %s", err, res.ValueJSON)
	}
}

func TestFSWriteRoots(t *testing.T) {
	wd, annealRoot := newEnv(t)
	// Inside the anneal root but not in scratch: readable, not writable.
	secret := filepath.Join(annealRoot, "sessions", "s1.jsonl")
	if err := os.MkdirAll(filepath.Dir(secret), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte(`{"role":"user"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := run(t, wd, "fs.write(\""+secret+"\", \"nope\")")
	if err == nil || !strings.Contains(err.Error(), "writable roots") {
		t.Fatalf("want write denial, got %v", err)
	}
	res, err := run(t, wd, "json.parse(fs.read(\""+secret+"\"))[\"role\"]")
	if err != nil || res.ValueJSON != `"user"` {
		t.Fatalf("state root read: %v %s", err, res.ValueJSON)
	}
	scratch := filepath.Join(annealRoot, "scratch", "notes", "n.txt")
	res, err = run(t, wd, "fs.write(\""+scratch+"\", \"ok\")")
	if err != nil || res.ValueJSON != "true" {
		t.Fatalf("scratch write: %v %s", err, res.ValueJSON)
	}
}

func TestFSReadOutsideRoots(t *testing.T) {
	wd, _ := newEnv(t)
	_, err := run(t, wd, "fs.read(\"/etc/passwd\")")
	if err == nil || !strings.Contains(err.Error(), "readable roots") {
		t.Fatalf("want read denial, got %v", err)
	}
}

func TestFSSecretSegmentsDenied(t *testing.T) {
	wd, _ := newEnv(t)
	// Even inside the workdir, secret dot-dirs stay off-limits.
	secret := filepath.Join(wd, ".ssh", "id_ed25519")
	if err := os.MkdirAll(filepath.Dir(secret), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := run(t, wd, "fs.read(\""+secret+"\")")
	if err == nil || !strings.Contains(err.Error(), "outside the script sandbox") {
		t.Fatalf("want sandbox denial, got %v", err)
	}
	_, err = run(t, wd, "fs.write(\".ssh/x\", \"y\")")
	if err == nil || !strings.Contains(err.Error(), "outside the script sandbox") {
		t.Fatalf("want sandbox denial on write, got %v", err)
	}
}

func TestFSSymlinkEscapeDenied(t *testing.T) {
	wd, _ := newEnv(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "target.txt"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(wd, "link")); err != nil {
		t.Fatal(err)
	}
	_, err := run(t, wd, "fs.read(\"link/target.txt\")")
	if err == nil || !strings.Contains(err.Error(), "readable roots") {
		t.Fatalf("want containment denial, got %v", err)
	}
}

func TestFSList(t *testing.T) {
	wd, _ := newEnv(t)
	if err := os.WriteFile(filepath.Join(wd, "a.txt"), []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := run(t, wd, `fs.list(".").filter(e => e.name == "a.txt")[0].size`)
	if err != nil {
		t.Fatalf("list: %v (%s)", err, res.Output)
	}
	if res.ValueJSON != "3" {
		t.Errorf("size = %s, want 3", res.ValueJSON)
	}
}

func TestOutputCap(t *testing.T) {
	wd, _ := newEnv(t)
	res, err := run(t, wd, `range(2000).each(i => print("0123456789"))`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Truncated || !strings.HasSuffix(res.Output, "[output truncated]") {
		t.Errorf("want truncated output, got %d bytes (trunc=%v)", len(res.Output), res.Truncated)
	}
	if len(res.Output) > MaxOutputBytes+100 {
		t.Errorf("output too long: %d", len(res.Output))
	}
}

func TestValueCap(t *testing.T) {
	wd, _ := newEnv(t)
	res, err := run(t, wd, "\"0123456789\".repeat(2000)")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Truncated || !strings.HasSuffix(res.ValueJSON, "[result truncated]") {
		t.Errorf("want truncated result, got %d bytes (trunc=%v)", len(res.ValueJSON), res.Truncated)
	}
}
