package main

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
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

func (s *shell) addUser(text string) {
	lbl := widget.NewLabel("> " + text)
	lbl.Wrapping = fyne.TextWrapWord
	lbl.TextStyle = fyne.TextStyle{Italic: true}
	lbl.Selectable = true
	// Thin accent bar marks the user's own turns in the transcript.
	bar := canvas.NewRectangle(theme.Color(theme.ColorNamePrimary))
	bar.SetMinSize(fyne.NewSize(3, 0))
	s.appendBlock(container.NewBorder(nil, nil, bar, nil, lbl))
}

func (s *shell) addMarkdown(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	// RichText is not selectable in Fyne, so the transcript uses plain
	// selectable labels: prose with markdown stripped, tables/code as
	// monospace blocks with intact alignment.
	for _, seg := range splitMarkdownSegments(text) {
		lbl := widget.NewLabel(seg.text)
		lbl.Wrapping = fyne.TextWrapWord
		lbl.Selectable = true
		if seg.mono {
			lbl.TextStyle = fyne.TextStyle{Monospace: true}
		}
		s.appendBlock(lbl)
	}
}

func (s *shell) addNotice(text string) {
	lbl := widget.NewLabel(text)
	lbl.Wrapping = fyne.TextWrapWord
	lbl.Importance = widget.LowImportance
	lbl.Selectable = true
	s.appendBlock(lbl)
}

func (s *shell) addTool(text string) {
	lbl := widget.NewLabel(text)
	lbl.Wrapping = fyne.TextWrapWord
	lbl.TextStyle = fyne.TextStyle{Monospace: true}
	lbl.Importance = widget.LowImportance
	lbl.Selectable = true
	s.appendBlock(lbl)
}

func (s *shell) appendBlock(obj fyne.CanvasObject) {
	// The streaming tail is always visually last.
	if s.tail != nil {
		s.chatBox.Remove(s.tail)
	}
	s.visible = append(s.visible, obj)
	s.chatBox.Add(obj)
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
	s.earlier.SetText(fmt.Sprintf("show %d earlier messages", len(s.hidden)))
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
	s.chatBox.RemoveAll()
	s.chatBox.Refresh()
	if s.earlier != nil {
		s.earlier.Hide()
	}
	s.perms.RemoveAll()
	s.perms.Refresh()
	s.permRows = map[string]fyne.CanvasObject{}
	s.todos.RemoveAll()
	s.todos.Refresh()
	if s.todoPane != nil {
		s.todoPane.Hide()
	}
}

func (s *shell) ensureTail() {
	if s.tail == nil {
		s.tailBuf = ""
		s.tail = widget.NewLabel("")
		s.tail.Wrapping = fyne.TextWrapWord
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

func (s *shell) renderBlock(b map[string]any) {
	switch str(b["kind"]) {
	case "user":
		s.addUser(str(b["text"]))
	case "assistant":
		s.addMarkdown(str(b["text"]))
	case "tool":
		s.addTool("🔧 " + str(b["name"]) + " " + oneLine(str(b["input"])))
	case "toolres":
		mark := "✔"
		if boolean(b["error"]) {
			mark = "✖"
		}
		s.addTool("  " + mark + " " + str(b["name"]) + ": " + oneLine(str(b["text"])))
	}
}
