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
