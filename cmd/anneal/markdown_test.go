package main

import (
	"strings"
	"testing"
)

func TestMarkdownToPlain(t *testing.T) {
	in := "# Title\n\nSome **bold** and *italic* and `code`.\n\n" +
		"- [ ] todo\n- [link](https://example.com)\n\n" +
		"```go\nfmt.Println(\"hi\")\n```\n\n> quoted\n"
	got := markdownToPlain(in)
	for _, want := range []string{"Title", "Some bold and italic and code.", "- [ ] todo",
		"- link (https://example.com)", `fmt.Println("hi")`, "quoted"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"**", "```", "](https"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("kept markdown syntax %q in:\n%s", unwanted, got)
		}
	}
}

func TestSplitMarkdownSegments(t *testing.T) {
	in := "Intro **text**.\n\n" +
		"| Stage | What happens |\n|---|---|\n| Propose | editors draft |\n| Probe | replay runs |\n\n" +
		"```go\nx := 1\n```\n\nOutro."
	segs := splitMarkdownSegments(in)
	if len(segs) != 4 {
		t.Fatalf("want prose/table/code/prose, got %d: %+v", len(segs), segs)
	}
	if segs[0].mono || !strings.Contains(segs[0].text, "Intro text") {
		t.Errorf("prose seg: %+v", segs[0])
	}
	if !segs[1].mono {
		t.Errorf("table seg must be mono: %+v", segs[1])
	}
	if !strings.Contains(segs[1].text, "Propose") || !strings.Contains(segs[1].text, "─") ||
		strings.Contains(segs[1].text, "|") {
		t.Errorf("table not reflowed: %q", segs[1].text)
	}
	// Columns align (mono rendering assumed): header row vs first data row.
	lines := strings.Split(segs[1].text, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected header/underline/data rows, got: %q", segs[1].text)
	}
	if idx := strings.Index(lines[0], "What happens"); idx != strings.Index(lines[2], "editors draft") {
		t.Errorf("columns misaligned:\n%s", segs[1].text)
	}
	if !segs[2].mono || !strings.Contains(segs[2].text, "x := 1") {
		t.Errorf("code seg: %+v", segs[2])
	}
	if segs[3].mono || segs[3].text != "Outro." {
		t.Errorf("trailing prose: %+v", segs[3])
	}
}

func TestSplitMarkdownLonePipeLineIsProse(t *testing.T) {
	segs := splitMarkdownSegments("|a|b|\nplain line")
	for _, s := range segs {
		if s.mono {
			t.Fatalf("lone pipe row must fall back to prose: %+v", segs)
		}
	}
	if len(segs) == 0 || !strings.Contains(segs[0].text, "|a|b|") {
		t.Fatalf("pipe line lost: %+v", segs)
	}
}

func TestChunkCutPrefersBoundary(t *testing.T) {
	s := strings.Repeat("word ", 600) // 3000 bytes
	cut := chunkCut(s, 2000)
	if cut <= 0 || cut > 2000 {
		t.Fatalf("cut=%d", cut)
	}
	if cut < len(s) && !strings.HasPrefix(s[cut:], "word") {
		t.Fatalf("split mid-word: %q", s[cut:cut+8])
	}
	// Multibyte runes must not be split.
	runes := strings.Repeat("é", 1500) // 3000 bytes
	cut = chunkCut(runes, 2000)
	if cut%2 != 0 {
		t.Fatalf("split inside a rune: cut=%d", cut)
	}
	if cut >= len(runes) || !strings.HasPrefix(runes[cut:], "é") {
		t.Fatalf("bad rune boundary: %q", runes[cut:])
	}
}
