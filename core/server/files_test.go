package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/uidbz/anneal/core/client"
)

func TestCleanWorkRel(t *testing.T) {
	cases := []struct {
		in   string
		want string // "" = must be rejected
	}{
		{"file.txt", "file.txt"},
		{"a/b/c.txt", "a/b/c.txt"},
		{"./a", "a"},
		{"a//b", "a/b"},
		{"a/./b", "a/b"},
		{`a\b`, "a/b"},          // backslashes normalize, not separate escapes
		{"/etc/passwd", ""},     // absolute
		{"\\server\\share", ""}, // UNC-ish
		{"C:/Windows", ""},      // drive form
		{"C:\\Windows", ""},     // drive form
		{"..", ""},              // escape
		{"../x", ""},            // escape
		{"a/../../x", ""},       // escape after clean
		{"a/../b", "b"},         // sibling, stays inside: allowed
		{".", ""},               // must name something inside the root
		{"./", ""},
		{"", ""},       // empty
		{"a\x00b", ""}, // NUL
	}
	for _, c := range cases {
		got, err := cleanWorkRel(c.in)
		if c.want == "" {
			if err == nil {
				t.Errorf("cleanWorkRel(%q) = %q, want rejection", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("cleanWorkRel(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("cleanWorkRel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// mkTarGz builds an archive from tar.Header/name/body triples so tests can
// plant hostile member names and types.
func mkTarGz(t *testing.T, hdrs []tar.Header, bodies map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for _, h := range hdrs {
		if h.Size > 0 {
			h.Format = tar.FormatUSTAR
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatalf("write header %q: %v", h.Name, err)
		}
		if body := bodies[h.Name]; body != "" {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatalf("write body %q: %v", h.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func fileHdr(name string, mode int64, body string) tar.Header {
	return tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: mode,
		Size: int64(len(body)), Format: tar.FormatUSTAR}
}

func TestExtractTarGz(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "tree")
	archive := mkTarGz(t, []tar.Header{
		{Name: "sub/", Typeflag: tar.TypeDir, Mode: 0o755},
		fileHdr("a.txt", 0o644, "alpha"),
		fileHdr("sub/b.txt", 0o600, "beta"),
	}, map[string]string{"a.txt": "alpha", "sub/b.txt": "beta"})

	entries, err := extractTarGz(dest, archive)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if entries != 3 {
		t.Errorf("entries = %d, want 3", entries)
	}
	for name, want := range map[string]string{"a.txt": "alpha", "sub/b.txt": "beta"} {
		got, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v", name, got, err)
		}
	}
	if fi, err := os.Stat(filepath.Join(dest, "sub", "b.txt")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode not preserved: %v %v", fi.Mode(), err)
	}
}

func TestExtractTarGzRefusesHostileArchives(t *testing.T) {
	hostile := map[string][]tar.Header{
		"symlink":  {{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}},
		"hardlink": {{Name: "hard", Typeflag: tar.TypeLink, Linkname: "a.txt"}},
		"dotdot":   {fileHdr("../escape", 0o644, "x")},
		"absolute": {fileHdr("/etc/escape", 0o644, "x")},
		"fifo":     {{Name: "pipe", Typeflag: tar.TypeFifo}},
	}
	for name, hdrs := range hostile {
		t.Run(name, func(t *testing.T) {
			dest := t.TempDir()
			if _, err := extractTarGz(dest, mkTarGz(t, hdrs, map[string]string{"../escape": "x", "/etc/escape": "x"})); err == nil {
				t.Fatalf("archive with %s entry extracted without error", name)
			}
		})
	}
}

func TestExtractTarGzEntryCap(t *testing.T) {
	hdrs := make([]tar.Header, maxArchiveEntries+1)
	for i := range hdrs {
		hdrs[i] = fileHdr(itoa(i), 0o644, "")
	}
	dest := t.TempDir()
	if _, err := extractTarGz(dest, mkTarGz(t, hdrs, nil)); err == nil {
		t.Fatal("archive over the entry cap accepted")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "f0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return "f" + string(b)
}

func TestArchiveRoundtripUnit(t *testing.T) {
	src := t.TempDir()
	mk := func(name, body string) {
		p := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("README", "readme")
	mk("src/main.go", "package main")
	mk("src/util/help.go", "package util")

	archive, err := archiveTarGz(filepath.Join(src, "src"))
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	dst := t.TempDir()
	if _, err := extractTarGz(dst, archive); err != nil {
		t.Fatalf("extract: %v", err)
	}
	for name, want := range map[string]string{"main.go": "package main", "util/help.go": "package util"} {
		got, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil || string(got) != want {
			t.Errorf("roundtrip %s = %q, %v", name, got, err)
		}
	}

	// A single file archives as one member named after its base name.
	archive, err = archiveTarGz(filepath.Join(src, "README"))
	if err != nil {
		t.Fatalf("archive file: %v", err)
	}
	dst2 := t.TempDir()
	if _, err := extractTarGz(dst2, archive); err != nil {
		t.Fatalf("extract file: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dst2, "README")); err != nil || string(got) != "readme" {
		t.Errorf("single-file roundtrip = %q, %v", got, err)
	}
}

func TestArchiveTarGzRefusesSymlinkTree(t *testing.T) {
	src := t.TempDir()
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "sneaky")); err != nil {
		t.Fatal(err)
	}
	if _, err := archiveTarGz(src); err == nil {
		t.Fatal("tree with symlink archived without error")
	}
}

func TestPrepareWorkDir(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)
	s, _ := startTest(t, nil) // work root = the cwd the runner was built with
	if s.App.Runner.Cwd() == "" {
		t.Fatal("runner cwd empty")
	}
	root := s.App.Runner.Cwd()

	abs, err := s.prepareWorkDir("runs/nested task")
	if err != nil {
		t.Fatalf("prepareWorkDir: %v", err)
	}
	if abs != filepath.Join(root, "runs/nested task") {
		t.Errorf("prepared %q, want under %q", abs, root)
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		t.Errorf("work dir not created: %v %v", fi, err)
	}
	for _, bad := range []string{"/etc", "..", "../x", "a/../../b"} {
		if got, err := s.prepareWorkDir(bad); err == nil {
			t.Errorf("prepareWorkDir(%q) = %q, want rejection", bad, got)
		}
	}
}

// Protocol-level roundtrip: the work root is the dir the server process
// started in (runner cwd), so chdir the test process first.
func TestFilesPutGetRoundtrip(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)
	_, c := startTest(t, nil)

	archive := mkTarGz(t, []tar.Header{
		fileHdr("hello.txt", 0o644, "hi"),
		{Name: "docs/", Typeflag: tar.TypeDir, Mode: 0o755},
		fileHdr("docs/note.md", 0o644, "# note"),
	}, map[string]string{"hello.txt": "hi", "docs/note.md": "# note"})

	var put struct {
		OK      bool   `json:"ok"`
		Path    string `json:"path"`
		Entries int    `json:"entries"`
	}
	callIntoOK(t, c, "files.put", map[string]any{
		"path": "proj/tree",
		"data": base64.StdEncoding.EncodeToString(archive),
	}, &put)
	if !put.OK || put.Path != "proj/tree" || put.Entries != 3 {
		t.Fatalf("files.put = %+v", put)
	}
	for name, want := range map[string]string{"hello.txt": "hi", "docs/note.md": "# note"} {
		got, err := os.ReadFile(filepath.Join(work, "proj", "tree", name))
		if err != nil || string(got) != want {
			t.Errorf("on disk %s = %q, %v", name, got, err)
		}
	}

	var get struct {
		OK    bool   `json:"ok"`
		Path  string `json:"path"`
		Data  string `json:"data"`
		Bytes int    `json:"bytes"`
	}
	callIntoOK(t, c, "files.get", map[string]any{"path": "proj/tree"}, &get)
	if !get.OK || get.Path != "proj/tree" || get.Bytes == 0 {
		t.Fatalf("files.get = %+v", get)
	}
	raw, err := base64.StdEncoding.DecodeString(get.Data)
	if err != nil || len(raw) != get.Bytes {
		t.Fatalf("data/bytes mismatch: %v", err)
	}
	back := filepath.Join(work, "collected")
	if _, err := extractTarGz(back, raw); err != nil {
		t.Fatalf("re-extract: %v", err)
	}
	for name, want := range map[string]string{"hello.txt": "hi", "docs/note.md": "# note"} {
		got, err := os.ReadFile(filepath.Join(back, name))
		if err != nil || string(got) != want {
			t.Errorf("roundtrip %s = %q, %v", name, got, err)
		}
	}

	// Hostile paths are rejected at the RPC boundary.
	for _, bad := range []string{"/etc", "..", "a/../../b"} {
		if _, err := c.Call(t.Context(), "files.get", map[string]any{"path": bad}); err == nil {
			t.Errorf("files.get(%q) accepted", bad)
		}
		if _, err := c.Call(t.Context(), "files.put", map[string]any{"path": bad, "data": get.Data}); err == nil {
			t.Errorf("files.put(%q) accepted", bad)
		}
	}
}

func TestFilesPutRejectsSymlinkArchive(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)
	_, c := startTest(t, nil)

	archive := mkTarGz(t, []tar.Header{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}}, nil)
	if _, err := c.Call(t.Context(), "files.put", map[string]any{
		"path": "tree",
		"data": base64.StdEncoding.EncodeToString(archive),
	}); err == nil {
		t.Fatal("files.put with symlink member accepted")
	}
	if _, err := os.Lstat(filepath.Join(work, "tree", "link")); err == nil {
		t.Error("symlink landed despite rejection")
	}
}

// agent.send validates work before touching the runner: an escaping path
// is a -32602, and no run or directory is created for it.
func TestAgentSendWorkValidation(t *testing.T) {
	work := t.TempDir()
	t.Chdir(work)
	_, c := startTest(t, nil)
	for _, bad := range []string{"../escape", "/etc", "a/../../b"} {
		if _, err := c.Call(t.Context(), "agent.send", map[string]any{"text": "hi", "work": bad}); err == nil {
			t.Errorf("agent.send work=%q accepted", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(work), "escape")); err == nil {
		t.Error("escaped work dir created outside the root")
	}
}

// callIntoOK is callOK plus decode — kept here so files tests stay readable.
func callIntoOK(t *testing.T, c *client.Client, method string, params, out any) {
	t.Helper()
	raw := callOK(t, c, method, params)
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("%s result: %v", method, err)
	}
}
