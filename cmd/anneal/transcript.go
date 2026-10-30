package main

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// defaultVisibleBlocks bounds how many transcript widgets are laid out at
// once. Older blocks stay in memory and can be restored on demand.
const defaultVisibleBlocks = 300

// streamChunkBytes bounds the mutable streaming label. A word-wrapped Fyne
// label re-lays out its whole text on every SetText, so letting one label grow
// to the size of a long answer freezes the UI (and the stop button) for
// seconds. Streamed text is frozen into immutable chunks instead; only the
// small tail label is re-laid out.
const streamChunkBytes = 2000

// addUser appends one user turn (caption + card) and closes the assistant
// turn so the next answer gets its own caption.
func (s *shell) addUser(text string) {
	s.turn = turnUser
	s.appendBlock(s.userBlock(text))
}

// addMarkdown appends an assistant answer as prose and code/table blocks.
// Code blocks go on a code surface; the caption is attached to the first
// block so a turn still counts as one visible transcript block.
func (s *shell) addMarkdown(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	segs := splitMarkdownSegments(text)
	for i, seg := range segs {
		obj := s.markdownBlock(seg)
		if i == 0 && s.turn != turnAssistant {
			obj = container.NewVBox(
				container.New(&padLayout{l: 0, r: 0, t: 0, b: 2}, turnCaption("Anneal", true)),
				obj,
			)
		}
		s.appendBlock(obj)
	}
	s.turn = turnAssistant
}

// addNotice appends a system line. The level is inferred from the legacy
// marker prefixes so old call sites keep working while they are migrated.
func (s *shell) addNotice(text string) {
	switch {
	case strings.HasPrefix(text, "⚠"):
		s.notice(noticeWarn, strings.TrimSpace(strings.TrimPrefix(text, "⚠")))
	case strings.HasPrefix(text, "✖"):
		s.notice(noticeError, strings.TrimSpace(strings.TrimPrefix(text, "✖")))
	case strings.HasPrefix(text, "✔"):
		s.notice(noticeSuccess, strings.TrimSpace(strings.TrimPrefix(text, "✔")))
	default:
		s.notice(noticeInfo, text)
	}
}

func (s *shell) notice(k noticeKind, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	s.appendBlock(s.noticeBlock(k, text))
}

// addTool starts a tool row and remembers it by call id so the result can
// update the same row.
func (s *shell) addTool(id, name, input string) *toolRow {
	row := newToolRow(name, toolSummary(input))
	obj := fyne.CanvasObject(row)
	if s.turn != turnAssistant {
		obj = container.NewVBox(
			container.New(&padLayout{l: 0, r: 0, t: 0, b: 2}, turnCaption("Anneal", true)),
			row,
		)
	}
	s.appendBlock(obj)
	s.turn = turnAssistant
	if id == "" {
		// Replayed calls carry no id; results pair with them in order.
		s.pendingTools = append(s.pendingTools, row)
		return row
	}
	if s.toolRows == nil {
		s.toolRows = map[string]*toolRow{}
	}
	s.toolRows[id] = row
	return row
}

// finishTool updates the row created by addTool (by call id). A result with
// no matching call (replay of an unusual transcript) gets its own row.
func (s *shell) finishTool(id, name, preview string, isErr bool) *toolRow {
	var row *toolRow
	if id != "" {
		row = s.toolRows[id]
		delete(s.toolRows, id)
	}
	if row == nil && len(s.pendingTools) > 0 {
		row = s.pendingTools[0]
		s.pendingTools = s.pendingTools[1:]
	}
	if row == nil {
		row = s.addTool("", name, "")
		s.pendingTools = s.pendingTools[:len(s.pendingTools)-1]
	}
	row.finish(preview, isErr, time.Since(row.started))
	return row
}

// progress returns a live line for key, creating it on first use. Repeated
// improve/review steps update it in place.
func (s *shell) progress(key, text string) *progressLine {
	if s.progressLines == nil {
		s.progressLines = map[string]*progressLine{}
	}
	line, ok := s.progressLines[key]
	if !ok {
		line = newProgressLine(text)
		s.appendBlock(line.object)
		s.progressLines[key] = line
		return line
	}
	line.update(text, noticeProgress)
	return line
}

// finishProgress turns a live line into a final notice and forgets it, so the
// next pass starts a new line.
func (s *shell) finishProgress(key string, k noticeKind, text string) {
	if line, ok := s.progressLines[key]; ok {
		line.update(text, k)
		delete(s.progressLines, key)
		return
	}
	if k == noticeInfo {
		s.notice(noticeInfo, text)
		return
	}
	s.notice(k, text)
}

// appendBlock adds a transcript block and keeps the streaming tail last.
func (s *shell) appendBlock(obj fyne.CanvasObject) {
	if s.welcome != nil {
		s.welcome.Hide()
	}
	if s.tail != nil {
		s.chatBox.Remove(s.tail)
	}
	for _, chunk := range s.streamBlocks {
		s.chatBox.Remove(chunk)
	}
	s.visible = append(s.visible, obj)
	s.chatBox.Add(obj)
	for _, chunk := range s.streamBlocks {
		s.chatBox.Add(chunk)
	}
	if s.tail != nil {
		s.chatBox.Add(s.tail)
	}
	s.trimVisible()
	s.chatBox.Refresh()
	if s.scroll != nil {
		s.scroll.ScrollToBottom()
	}
}

// trimVisible bounds the rendered transcript. Older blocks are detached from
// the layout (not discarded) and can be restored with "show earlier messages",
// so long sessions do not re-layout thousands of widgets on every event.
func (s *shell) trimVisible() {
	if s.maxVisible <= 0 {
		return
	}
	for len(s.visible) > s.maxVisible {
		obj := s.visible[0]
		s.visible = s.visible[1:]
		s.hidden = append(s.hidden, obj)
		s.chatBox.Remove(obj)
	}
	s.updateEarlier()
}

func (s *shell) updateEarlier() {
	if s.earlier == nil {
		return
	}
	if len(s.hidden) == 0 {
		s.earlier.Hide()
		return
	}
	s.earlier.SetText(fmt.Sprintf("Show %d earlier messages", len(s.hidden)))
	s.earlier.Show()
}

func (s *shell) showEarlier() {
	if len(s.hidden) == 0 {
		return
	}
	n := s.maxVisible
	if n <= 0 || n > len(s.hidden) {
		n = len(s.hidden)
	}
	restore := s.hidden[len(s.hidden)-n:]
	s.hidden = s.hidden[:len(s.hidden)-n]
	s.visible = append(append([]fyne.CanvasObject{}, restore...), s.visible...)
	s.chatBox.RemoveAll()
	for _, obj := range s.visible {
		// Detached widgets missed the theme-change repaint; refresh them now.
		obj.Refresh()
		s.chatBox.Add(obj)
	}
	for _, obj := range s.streamBlocks {
		s.chatBox.Add(obj)
	}
	if s.tail != nil {
		s.chatBox.Add(s.tail)
	}
	s.chatBox.Refresh()
	s.updateEarlier()
	if s.scroll != nil {
		s.scroll.ScrollToTop()
	}
}

func (s *shell) resetChat() {
	s.tail = nil
	s.tailBuf = ""
	s.streamBlocks = nil
	s.visible = nil
	s.hidden = nil
	s.turn = turnNone
	s.toolRows = map[string]*toolRow{}
	s.pendingTools = nil
	s.progressLines = map[string]*progressLine{}
	s.chatBox.RemoveAll()
	s.chatBox.Refresh()
	if s.earlier != nil {
		s.earlier.Hide()
	}
	s.pendingApprovals = nil
	s.permRows = map[string]fyne.CanvasObject{}
	s.dock.clear()
	s.todos.RemoveAll()
	s.todos.Refresh()
	if s.todoPane != nil {
		s.todoPane.Hide()
	}
	if s.welcome != nil {
		s.welcome.Show()
	}
}

func (s *shell) ensureTail() {
	if s.tail == nil {
		s.tailBuf = ""
		s.tail = widget.NewLabel("")
		s.tail.Wrapping = fyne.TextWrapWord
		s.tail.Selectable = true
		s.chatBox.Add(s.tail)
		s.chatBox.Refresh()
	}
}

// flushTail renders the buffered stream text. Once the tail reaches
// streamChunkBytes it is frozen (the label is left in place and a fresh tail
// starts), so no single label ever gets large enough for its word-wrap layout
// to block the UI thread. Frozen chunks are temporary and removed by
// closeTail, which replaces them with the final selectable block.
func (s *shell) flushTail() {
	if s.tail == nil {
		return
	}
	for len(s.tailBuf) >= streamChunkBytes {
		cut := chunkCut(s.tailBuf, streamChunkBytes)
		chunk, rest := s.tailBuf[:cut], s.tailBuf[cut:]
		s.tail.SetText(chunk)
		s.tail.Refresh()
		s.streamBlocks = append(s.streamBlocks, s.tail)
		s.tail = widget.NewLabel(rest)
		s.tail.Wrapping = fyne.TextWrapWord
		s.tailBuf = rest
		s.chatBox.Add(s.tail)
	}
	s.tail.SetText(s.tailBuf)
	s.tail.Refresh()
	s.chatBox.Refresh()
	if s.scroll != nil {
		s.scroll.ScrollToBottom()
	}
}

// chunkCut picks a chunk boundary near max, preferring a line or word break
// and never splitting a UTF-8 rune.
func chunkCut(s string, max int) int {
	cut := max
	if i := strings.LastIndexByte(s[:cut], '\n'); i > cut/2 {
		cut = i + 1
	} else if i := strings.LastIndexByte(s[:cut], ' '); i > cut/2 {
		cut = i + 1
	}
	for cut > 0 && cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut <= 0 {
		return max
	}
	return cut
}

func (s *shell) closeTail(final string) {
	removed := false
	if s.tail != nil {
		s.chatBox.Remove(s.tail)
		s.tail = nil
		removed = true
	}
	for _, obj := range s.streamBlocks {
		s.chatBox.Remove(obj)
		removed = true
	}
	s.tailBuf = ""
	s.streamBlocks = nil
	if removed {
		s.chatBox.Refresh()
	}
	if strings.TrimSpace(final) != "" {
		s.addMarkdown(final)
	}
}

// renderBlock replays one stored transcript block.
func (s *shell) renderBlock(b map[string]any) {
	switch str(b["kind"]) {
	case "user":
		s.addUser(str(b["text"]))
	case "assistant":
		s.addMarkdown(str(b["text"]))
	case "tool":
		s.addTool("", str(b["name"]), str(b["input"]))
	case "toolres":
		// Stored results carry no call id, so replay pairs them with the
		// oldest open tool call (the order they were produced in).
		s.finishTool("", str(b["name"]), str(b["text"]), boolean(b["error"]))
	}
}

// welcomeView is the empty-session state: product name, a hint, and the few
// keys worth learning first.
func (s *shell) welcomeView() fyne.CanvasObject {
	title := newThemedText("Anneal", theme.ColorNameForeground)
	title.size = theme.SizeNameHeadingText
	title.bold = true
	sub := newThemedText("Describe a task to get started, or type / for commands.", colorMuted)
	hints := newThemedText("Ctrl+N new session · Ctrl+B sessions · Ctrl+I improve harness", colorMuted)
	hints.size = theme.SizeNameCaptionText
	body := container.NewVBox(
		container.NewCenter(title),
		container.NewCenter(sub),
		vgap(spaceS),
		container.NewCenter(hints),
	)
	return container.NewCenter(body)
}
