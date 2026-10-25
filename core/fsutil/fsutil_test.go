package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	if err := WriteFileAtomic(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "one" {
		t.Fatalf("content: %q %v", b, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("perm: %v %v", info.Mode().Perm(), err)
	}
	if err := WriteFileAtomic(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "two" {
		t.Fatalf("overwrite: %q", b)
	}
	// No temp files left behind.
	ents, _ := os.ReadDir(filepath.Dir(path))
	if len(ents) != 1 {
		t.Fatalf("leftover temp files: %v", ents)
	}
}

func TestCopyFlatDir(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "system.md"), []byte("rule"), 0o600)
	os.WriteFile(filepath.Join(src, "notes.txt"), []byte("txt"), 0o644)
	os.MkdirAll(filepath.Join(src, "sub"), 0o755)
	os.WriteFile(filepath.Join(src, "sub", "nested.md"), []byte("nested"), 0o644)

	dst := filepath.Join(t.TempDir(), "overlay")
	if err := CopyFlatDir(src, dst, func(name string) bool { return filepath.Ext(name) == ".md" }); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "system.md")); err != nil || string(b) != "rule" {
		t.Fatalf("copied content: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "notes.txt")); !os.IsNotExist(err) {
		t.Fatal("filter ignored")
	}
	if _, err := os.Stat(filepath.Join(dst, "sub")); !os.IsNotExist(err) {
		t.Fatal("nested dir copied")
	}
}
