package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fyagent/core/config"
	"fyagent/core/paths"
)

func testStore(t *testing.T) *Store {
	t.Setenv("FYAGENT_HOME", t.TempDir())
	return NewStore(config.Skills{Enabled: true})
}

func createOp() *Op {
	return &Op{Action: Create, Name: "go-test-fixture",
		Description: "how to run the flaky fixture suite",
		Content:     "## Steps\n1. run `make test` with GOFLAGS"}
}

func TestCreateListPatchRemove(t *testing.T) {
	s := testStore(t)
	if _, err := s.Apply(createOp()); err != nil {
		t.Fatal(err)
	}
	// duplicate create rejected
	if err := s.Validate(createOp()); err == nil {
		t.Fatal("duplicate create allowed")
	}
	list := s.List()
	if len(list) != 1 || list[0].Name != "go-test-fixture" {
		t.Fatalf("list: %+v", list)
	}
	if !strings.Contains(list[0].Description, "flaky") {
		t.Fatalf("description not parsed: %+v", list[0])
	}
	view, err := s.View("go-test-fixture")
	if err != nil || !strings.Contains(view, "name: go-test-fixture") {
		t.Fatalf("view: %q %v", view, err)
	}
	if _, err := s.Apply(&Op{Action: Patch, Name: "go-test-fixture",
		Old: "make test", New: "make test -count=1"}); err != nil {
		t.Fatal(err)
	}
	view, _ = s.View("go-test-fixture")
	if !strings.Contains(view, "-count=1") {
		t.Fatal("patch missing")
	}
	if _, err := s.Apply(&Op{Action: Remove, Name: "go-test-fixture"}); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 0 {
		t.Fatal("remove left skill")
	}
	ents, _ := os.ReadDir(paths.SkillsArchive())
	if len(ents) != 1 || !strings.HasPrefix(ents[0].Name(), "go-test-fixture-") {
		t.Fatalf("archive: %+v", ents)
	}
}

func TestValidation(t *testing.T) {
	s := testStore(t)
	if err := s.Validate(&Op{Action: Create, Name: "Bad Name!", Description: "x", Content: "y"}); err == nil {
		t.Fatal("bad slug accepted")
	}
	if err := s.Validate(&Op{Action: Create, Name: "ok-name", Description: "", Content: "y"}); err == nil {
		t.Fatal("missing description accepted")
	}
	if _, err := s.Apply(&Op{Action: WriteFile, Name: "ok-name", Path: "../evil", Content: "x"}); err == nil {
		t.Fatal("path escape accepted")
	}
	if err := s.Validate(&Op{Action: Patch, Name: "nope", Old: "a", New: "b"}); err == nil {
		t.Fatal("patch on missing skill accepted")
	}
}

func TestWriteFileRoundtrip(t *testing.T) {
	s := testStore(t)
	s.Apply(createOp())
	if _, err := s.Apply(&Op{Action: WriteFile, Name: "go-test-fixture",
		Path: "references/notes.md", Content: "# deep notes"}); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(paths.Skills(), "go-test-fixture", "references", "notes.md")
	data, err := os.ReadFile(f)
	if err != nil || !strings.Contains(string(data), "deep notes") {
		t.Fatalf("support file: %q %v", data, err)
	}
}

func TestStageResolveSkill(t *testing.T) {
	s := testStore(t)
	op, err := s.Stage(createOp())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 0 {
		t.Fatal("staged skill applied early")
	}
	if s.Preview(op) == "" {
		t.Fatal("empty preview")
	}
	if _, res, err := s.Resolve(op.ID, true); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(res, "created skill") {
		t.Fatalf("res: %q", res)
	}
	if len(s.List()) != 1 {
		t.Fatal("approve did not apply")
	}
}
