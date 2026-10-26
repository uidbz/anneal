package main

import (
	"regexp"
	"strings"
)

// markdownReplacers strip the lightweight markdown syntax that the model
// uses, keeping the words, lists, tables and code. The transcript renders
// assistant answers as selectable labels instead of RichText (Fyne's RichText
// cannot be selected), so this is the trade-off that keeps copy/paste working.
var markdownReplacers = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`), "$1 ($2)"},
	{regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`), "$1 ($2)"},
	{regexp.MustCompile(`\*\*([^*]+)\*\*`), "$1"},
	{regexp.MustCompile(`__([^_]+)__`), "$1"},
	{regexp.MustCompile(`\*([^*\n]+)\*`), "$1"},
	{regexp.MustCompile("`([^`]+)`"), "$1"},
	{regexp.MustCompile(`~~([^~]+)~~`), "$1"},
	{regexp.MustCompile(`<br\s*/?>`), "\n"},
}

var (
	headingRe = regexp.MustCompile(`^\s{0,3}(#{1,6})\s+(.*)$`)
	quoteRe   = regexp.MustCompile(`^\s*>\s?`)
)

// isRule reports a markdown horizontal rule (---, ***, ___) with optional
// spaces. Go's regexp has no backreferences, so this is checked by hand.
func isRule(line string) bool {
	t := strings.TrimSpace(line)
	if len(t) < 3 {
		return false
	}
	c := t[0]
	if c != '-' && c != '*' && c != '_' {
		return false
	}
	for i := 0; i < len(t); i++ {
		if t[i] != c && t[i] != ' ' {
			return false
		}
	}
	return true
}

// markdownToPlain normalizes common markdown into readable, copyable text.
func markdownToPlain(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}
		if isRule(line) {
			out = append(out, strings.Repeat("─", 40))
			continue
		}
		if m := headingRe.FindStringSubmatch(line); m != nil {
			out = append(out, strings.Repeat(" ", len(m[1])-1)+m[2])
			continue
		}
		// Quote markers stay visible but compact.
		line = quoteRe.ReplaceAllString(line, "")
		for _, r := range markdownReplacers {
			line = r.re.ReplaceAllString(line, r.repl)
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
