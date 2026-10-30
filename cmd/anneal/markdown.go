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

// mdSegment is one transcript block: proportional prose (markdown
// stripped) or a monospace block — fenced code or a column-aligned table.
// Alignment survives wrapping poorly, so tables/code get their own label.
type mdSegment struct {
	text string
	mono bool
}

// splitMarkdownSegments walks raw markdown into prose/code/table runs and
// renders each appropriately. This is what makes the assistant's frequent
// pipe tables readable in the selectable-label transcript.
func splitMarkdownSegments(src string) []mdSegment {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	var segs []mdSegment
	mode := 0 // 0=prose 1=code 2=table
	var cur []string
	flush := func() {
		body := strings.Join(cur, "\n")
		switch mode {
		case 1:
			if t := strings.TrimRight(body, "\n"); strings.TrimSpace(t) != "" {
				segs = append(segs, mdSegment{t, true})
			}
		case 2:
			if grid := alignTable(cur); grid != "" {
				segs = append(segs, mdSegment{grid, true})
			} else if t := markdownToPlain(body); t != "" {
				segs = append(segs, mdSegment{t, false}) // lone |line|: prose
			}
		default:
			if t := markdownToPlain(body); t != "" {
				segs = append(segs, mdSegment{t, false})
			}
		}
		cur = nil
	}
	inFence := false
	for _, ln := range strings.Split(src, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			if inFence {
				flush() // close code run
				mode = 0
			} else {
				flush() // close prose/table
				mode = 1
			}
			inFence = !inFence
			continue
		}
		if inFence {
			cur = append(cur, ln)
			continue
		}
		if isTableRow(t) {
			if mode != 2 {
				flush()
				mode = 2
			}
			cur = append(cur, t)
			continue
		}
		if mode != 0 {
			flush()
			mode = 0
		}
		cur = append(cur, ln)
	}
	flush()
	return segs
}

// isTableRow detects a pipe table row: leading and trailing pipe with at
// least one interior cell boundary.
func isTableRow(t string) bool {
	if len(t) < 4 || !strings.HasPrefix(t, "|") || !strings.HasSuffix(t, "|") {
		return false
	}
	return strings.Count(t, "|") >= 3
}

// isSepCell matches table separator cells (---, :--, :-:, --:).
func isSepCell(c string) bool {
	if c == "" {
		return false
	}
	for i := 0; i < len(c); i++ {
		if c[i] != '-' && c[i] != ':' {
			return false
		}
	}
	return true
}

// alignTable reflows pipe-table rows into a column-aligned grid (monospace
// rendering assumed). Separator rows are dropped; fewer than two data rows
// yields "" so the caller can fall back to prose.
func alignTable(rows []string) string {
	var grid [][]string
	for _, r := range rows {
		cells := strings.Split(strings.Trim(r, "|"), "|")
		sep := true
		for i, c := range cells {
			// Table segments bypass markdownToPlain, so strip inline
			// markers per cell (**bold**, `code`, links).
			c = strings.TrimSpace(c)
			for _, rp := range markdownReplacers {
				c = rp.re.ReplaceAllString(c, rp.repl)
			}
			cells[i] = strings.TrimSpace(c)
			if !isSepCell(c) {
				sep = false
			}
		}
		if !sep {
			grid = append(grid, cells)
		}
	}
	if len(grid) < 2 {
		return ""
	}
	var wid []int
	for _, row := range grid {
		for i, c := range row {
			if i == len(wid) {
				wid = append(wid, 0)
			}
			if n := len([]rune(c)); n > wid[i] {
				wid[i] = n
			}
		}
	}
	var b strings.Builder
	for i, row := range grid {
		for j, c := range row {
			b.WriteString(c)
			if j == len(row)-1 {
				break
			}
			b.WriteString(strings.Repeat(" ", wid[j]-len([]rune(c))+2))
		}
		if i == 0 {
			total := 0
			for _, w := range wid {
				total += w
			}
			total += 2 * (len(wid) - 1)
			if total > 72 {
				total = 72
			}
			b.WriteString("\n" + strings.Repeat("─", total))
		}
		if i < len(grid)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}
