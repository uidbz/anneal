package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// blockKind tracks the open transcript turn so the "Anneal" caption appears
// once per assistant turn instead of on every block.
type blockKind int

const (
	turnNone blockKind = iota
	turnUser
	turnAssistant
)

// turnCaption is the small muted speaker label above a turn.
func turnCaption(text string, accent bool) fyne.CanvasObject {
	lbl := newThemedText(text, colorMuted)
	lbl.bold = true
	lbl.size = theme.SizeNameCaptionText
	if !accent {
		return container.New(&padLayout{l: 0, r: 0, t: 0, b: 0}, lbl)
	}
	dot := newStatusDot()
	dot.setToken(theme.ColorNamePrimary)
	return container.New(&rowLayout{gap: spaceXS}, dot, lbl)
}

// selectableLabel is the transcript's text primitive: wrapping, selectable,
// optionally monospace.
func selectableLabel(text string, mono bool) *widget.Label {
	lbl := widget.NewLabel(text)
	lbl.Wrapping = fyne.TextWrapWord
	lbl.Selectable = true
	if mono {
		lbl.TextStyle = fyne.TextStyle{Monospace: true}
	}
	return lbl
}

// userBlock renders one user turn: caption + raised card.
func (s *shell) userBlock(text string) fyne.CanvasObject {
	body := selectableLabel(text, false)
	card := newSurface(colorRaised,
		container.New(&padLayout{l: spaceM, r: spaceM, t: spaceS, b: spaceS}, body)).
		border(colorBorder, 1)
	return container.NewVBox(
		container.New(&padLayout{l: spaceXS, r: 0, t: 0, b: 2}, turnCaption("You", false)),
		card,
	)
}

// markdownBlock renders one assistant segment; code and tables get a code
// surface so their alignment survives.
func (s *shell) markdownBlock(seg mdSegment) fyne.CanvasObject {
	lbl := selectableLabel(seg.text, seg.mono)
	if !seg.mono {
		return lbl
	}
	return newSurface(colorCode,
		container.New(&padLayout{l: spaceM, r: spaceM, t: spaceS, b: spaceS}, lbl)).
		border(colorBorder, 1)
}

// noticeKind selects the marker and color of a system line.
type noticeKind int

const (
	noticeInfo noticeKind = iota
	noticeSuccess
	noticeWarn
	noticeError
	noticeProgress
)

func noticeMarker(k noticeKind) (string, fyne.ThemeColorName) {
	switch k {
	case noticeSuccess:
		return "✔", colorOK
	case noticeWarn:
		return "⚠", colorWarn
	case noticeError:
		return "✖", colorErr
	case noticeProgress:
		return "●", theme.ColorNamePrimary
	default:
		return "", colorMuted
	}
}

// noticeBlock renders a muted system line with an optional marker.
func (s *shell) noticeBlock(k noticeKind, text string) fyne.CanvasObject {
	marker, token := noticeMarker(k)
	txt := selectableLabel(text, false)
	if k == noticeError {
		txt.Importance = widget.DangerImportance
	} else {
		txt.Importance = widget.LowImportance
	}
	if marker == "" {
		return container.New(&padLayout{l: spaceS, r: 0, t: 2, b: 2}, txt)
	}
	mark := newThemedText(marker, token)
	mark.bold = true
	return container.NewBorder(nil, nil,
		container.New(&padLayout{l: 0, r: spaceS, t: 0, b: 0}, mark), nil,
		container.New(&padLayout{l: 0, r: 0, t: 2, b: 2}, txt))
}

// progressLine is one live status line (improve/review) that updates in place
// instead of appending a new notice per step.
type progressLine struct {
	marker *themedText
	text   *themedText
	object fyne.CanvasObject
}

func newProgressLine(text string) *progressLine {
	p := &progressLine{
		marker: newThemedText("●", theme.ColorNamePrimary),
		text:   newThemedText(text, colorMuted),
	}
	p.marker.bold = true
	p.object = container.NewBorder(nil, nil,
		container.New(&padLayout{l: 0, r: spaceS, t: 0, b: 0}, p.marker), nil,
		container.New(&padLayout{l: 0, r: 0, t: 2, b: 2}, p.text))
	return p
}

func (p *progressLine) update(text string, k noticeKind) {
	p.text.SetText(text)
	marker, token := noticeMarker(k)
	if k == noticeProgress {
		token = theme.ColorNamePrimary
	}
	p.marker.token = token
	if marker == "" {
		marker = "●"
	}
	p.marker.SetText(marker)
	p.marker.Refresh()
	p.text.Refresh()
	p.object.Refresh()
}

// toolRow is one compact tool call: marker, name, argument summary, elapsed
// time, and a click-to-expand result preview. tool.result updates it in place
// instead of appending a second transcript line.
type toolRow struct {
	widget.BaseWidget
	name    string
	summary string
	started time.Time
	marker  *themedText
	nameLbl *themedText
	args    *themedText
	elapsed *themedText
	caret   *themedText
	preview *widget.Label
	body    *fyne.Container
	detail  *surface
	content *fyne.Container
	done    bool
	expand  bool
}

func newToolRow(name, summary string) *toolRow {
	r := &toolRow{
		name:    name,
		summary: summary,
		started: time.Now(),
		marker:  newThemedText("…", colorWarn),
		nameLbl: newThemedText(toolDisplayName(name), theme.ColorNameForeground),
		args:    newThemedText(summary, colorMuted),
		elapsed: newThemedText("", colorMuted),
		caret:   newThemedText("", colorMuted),
	}
	r.marker.bold = true
	r.nameLbl.bold = true
	header := container.NewBorder(nil, nil,
		container.New(&rowLayout{gap: spaceS}, r.marker, r.nameLbl, r.args),
		container.New(&rowLayout{gap: spaceS},
			container.New(&padLayout{l: spaceS, r: 0, t: 0, b: 0}, r.elapsed), r.caret), nil)
	r.body = container.New(&padLayout{l: 0, r: 0, t: 3, b: 3}, header)
	r.content = container.NewVBox(r.body)
	r.ExtendBaseWidget(r)
	return r
}

// finish marks the row done and stores the result preview.
func (r *toolRow) finish(preview string, isErr bool, took time.Duration) {
	r.done = true
	if isErr {
		r.marker.token = colorErr
		r.marker.SetText("✖")
	} else {
		r.marker.token = colorOK
		r.marker.SetText("✔")
	}
	r.marker.Refresh()
	if took >= 2*time.Millisecond {
		r.elapsed.SetText(formatElapsed(took))
	}
	if strings.TrimSpace(preview) != "" {
		r.preview = selectableLabel(preview, true)
		r.detail = newSurface(colorCode,
			container.New(&padLayout{l: spaceM, r: spaceM, t: spaceS, b: spaceS}, r.preview)).
			border(colorBorder, 1)
		r.detail.Hide()
		r.caret.SetText("▸")
		r.content.Add(r.detail)
		r.Refresh()
	}
	r.content.Refresh()
}

func (r *toolRow) setExpanded(open bool) {
	r.expand = open
	if r.detail == nil {
		return
	}
	if open {
		if r.preview == nil {
			return
		}
		r.detail.Show()
		r.caret.SetText("▾")
	} else {
		r.detail.Hide()
		r.caret.SetText("▸")
	}
	r.caret.Refresh()
	r.Refresh()
}

func (r *toolRow) Tapped(*fyne.PointEvent) {
	if !r.done || r.detail == nil {
		return
	}
	r.setExpanded(!r.expand)
}

func (r *toolRow) CreateRenderer() fyne.WidgetRenderer {
	return &toolRowRenderer{row: r}
}

type toolRowRenderer struct{ row *toolRow }

func (r *toolRowRenderer) Layout(size fyne.Size) {
	r.row.content.Move(fyne.NewPos(0, 0))
	r.row.content.Resize(size)
}

func (r *toolRowRenderer) MinSize() fyne.Size { return r.row.content.MinSize() }

func (r *toolRowRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.row.content}
}

func (r *toolRowRenderer) Refresh() {
	r.row.args.SetText(r.row.summary)
	r.row.content.Refresh()
}

func (r *toolRowRenderer) Destroy() {}

func formatElapsed(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

// toolDisplayName shortens MCP tool names (mcp__server__tool → server.tool).
func toolDisplayName(name string) string {
	if strings.HasPrefix(name, "mcp__") {
		parts := strings.Split(strings.TrimPrefix(name, "mcp__"), "__")
		if len(parts) == 2 {
			return parts[0] + "." + parts[1]
		}
	}
	return name
}

// toolSummary extracts the most useful single argument from a tool call for
// the compact transcript row.
func toolSummary(input string) string {
	var m map[string]any
	if json.Unmarshal([]byte(input), &m) != nil {
		return oneLine(input)
	}
	for _, key := range []string{"command", "file_path", "pattern", "query", "url", "path", "name"} {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return oneLine(s)
			}
		}
	}
	for _, v := range m {
		if s, ok := v.(string); ok && s != "" {
			return oneLine(s)
		}
	}
	return oneLine(input)
}

// taskRow is one todo line: a status marker whose top edge lines up with the
// first text line, and a selectable label that wraps in the remaining width.
type taskRow struct {
	widget.BaseWidget
	marker  *themedText
	label   *widget.Label
	markerW float32
	padTop  float32
}

func newTaskRow(glyph string, token fyne.ThemeColorName, content string, status string) *taskRow {
	r := &taskRow{
		marker:  newThemedText(glyph, token),
		label:   selectableLabel(content, false),
		markerW: theme.Size(theme.SizeNameText) + spaceS,
		padTop:  (theme.Size(theme.SizeNameText) - theme.Size(theme.SizeNameCaptionText)) / 2,
	}
	r.marker.bold = true
	switch status {
	case "done", "cancelled":
		r.label.Importance = widget.LowImportance
	case "in_progress":
		r.label.TextStyle = fyne.TextStyle{Bold: true}
	}
	r.ExtendBaseWidget(r)
	return r
}

func (r *taskRow) CreateRenderer() fyne.WidgetRenderer {
	r.marker.Resize(r.marker.MinSize())
	return &taskRowRenderer{row: r}
}

type taskRowRenderer struct{ row *taskRow }

func (r *taskRowRenderer) Layout(size fyne.Size) {
	r.row.marker.Move(fyne.NewPos(0, r.row.padTop))
	r.row.marker.Resize(r.row.marker.MinSize())
	r.row.label.Move(fyne.NewPos(r.row.markerW, 0))
	r.row.label.Resize(fyne.NewSize(size.Width-r.row.markerW, size.Height))
}

func (r *taskRowRenderer) MinSize() fyne.Size {
	ms := r.row.label.MinSize()
	return fyne.NewSize(r.row.markerW+ms.Width, ms.Height)
}

func (r *taskRowRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.row.marker, r.row.label}
}

func (r *taskRowRenderer) Refresh() {
	r.row.marker.Refresh()
	r.row.label.Refresh()
}

func (r *taskRowRenderer) Destroy() {}
