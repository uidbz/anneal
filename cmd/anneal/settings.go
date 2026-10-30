package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/uidbz/anneal/core/config"
	"github.com/uidbz/anneal/core/permission"
)

// settingsPanel is the Settings view: a form over a draft copy of the config.
// Save persists through App.SaveConfig (the same validation as the JSON
// editor); appearance changes apply immediately.
type settingsPanel struct {
	s      *shell
	object fyne.CanvasObject
	body   *fyne.Container

	draft  config.Config
	dirty  bool
	inSave bool
	bad    map[string]bool
	// controls maps form labels to their widgets (tests drive them).
	controls map[string]fyne.CanvasObject

	saveBtn   *widget.Button
	revertBtn *widget.Button
	banner    *fyne.Container
	bannerTxt *themedText
}

func newSettingsPanel(s *shell) *settingsPanel {
	p := &settingsPanel{s: s, bad: map[string]bool{}, controls: map[string]fyne.CanvasObject{}}
	p.draft = *s.a.Cfg
	p.object = p.buildPage()
	p.installHeader()
	return p
}

func (p *settingsPanel) Object() fyne.CanvasObject { return p.object }

// installHeader wires the view header actions.
func (p *settingsPanel) installHeader() {
	p.revertBtn = widget.NewButton("Revert", p.revert)
	p.revertBtn.Importance = widget.LowImportance
	p.revertBtn.Disable()
	p.saveBtn = widget.NewButton("Save", p.save)
	p.saveBtn.Importance = widget.HighImportance
	p.saveBtn.Disable()
	edit := labeledButton("Edit JSON…", theme.DocumentCreateIcon(), p.s.showSettingsJSON)
	p.s.settingsHeader.setActions(edit, p.revertBtn, p.saveBtn)
	p.s.settingsHeader.setSubtitle("~/.anneal/config.json")
}

// reload replaces the draft with the live config and rebuilds the form.
func (p *settingsPanel) reload() {
	p.draft = *p.s.a.Cfg
	p.dirty = false
	p.bad = map[string]bool{}
	if p.body != nil {
		p.fillBody()
	}
	p.updateButtons()
}

func (p *settingsPanel) revert() {
	p.reload()
	p.s.notice(noticeInfo, "Settings reverted.")
}

func (p *settingsPanel) save() {
	if p.settingsInvalid() {
		p.s.notice(noticeWarn, "Fix the highlighted settings before saving.")
		return
	}
	doc, err := json.MarshalIndent(&p.draft, "", "  ")
	if err != nil {
		p.s.notice(noticeError, "Settings: "+err.Error())
		return
	}
	p.inSave = true
	err = p.s.a.SaveConfig(string(doc))
	p.inSave = false
	if err != nil {
		p.s.notice(noticeWarn, "Settings: "+err.Error())
		return
	}
	p.draft = *p.s.a.Cfg
	p.dirty = false
	p.updateButtons()
	p.s.notice(noticeSuccess, "Settings saved.")
}

func (p *settingsPanel) settingsInvalid() bool {
	for _, bad := range p.bad {
		if bad {
			return true
		}
	}
	return false
}

func (p *settingsPanel) updateButtons() {
	if p.saveBtn == nil {
		return
	}
	if p.dirty && !p.settingsInvalid() {
		p.saveBtn.Enable()
	} else {
		p.saveBtn.Disable()
	}
	if p.dirty {
		p.revertBtn.Enable()
	} else {
		p.revertBtn.Disable()
	}
}

func (p *settingsPanel) changed() {
	if p.inSave {
		return
	}
	p.dirty = true
	p.updateButtons()
}

func (p *settingsPanel) setBad(key string, bad bool) {
	p.bad[key] = bad
	p.updateButtons()
}

// onExternalChange reacts to config.changed from other surfaces.
func (p *settingsPanel) onExternalChange(e map[string]any) {
	if p.inSave {
		p.reload()
		return
	}
	if p.dirty {
		p.showBanner("Configuration changed elsewhere.")
		return
	}
	p.reload()
}

func (p *settingsPanel) showBanner(text string) {
	p.bannerTxt.SetText(text)
	p.banner.Show()
	p.banner.Refresh()
}

// confirmDiscard asks before leaving the view with unsaved edits.
func (p *settingsPanel) confirmDiscard(then func()) {
	d := dialog.NewConfirm("Unsaved settings",
		"Discard your unsaved configuration changes?", func(ok bool) {
			if !ok {
				return
			}
			p.reload()
			then()
		}, p.s.win)
	d.Show()
}

// --- page construction -----------------------------------------------------

func (p *settingsPanel) buildPage() fyne.CanvasObject {
	p.body = container.New(&columnLayout{gap: spaceL})

	p.bannerTxt = newThemedText("", colorWarn)
	reloadBtn := widget.NewButton("Reload", func() { p.reload(); p.banner.Hide() })
	reloadBtn.Importance = widget.LowImportance
	p.banner = container.NewStack(
		newSurface(colorWarnWash, container.NewBorder(nil, nil,
			container.New(&padLayout{l: spaceM, r: spaceM, t: spaceS, b: spaceS}, p.bannerTxt),
			reloadBtn, nil)).border(colorWarn, 1),
	)
	p.banner.Hide()

	sc := container.NewVScroll(container.New(&padLayout{l: spaceXL, r: spaceXL, t: spaceL, b: spaceXL},
		container.New(&maxWidthLayout{max: 780}, p.body)))
	p.fillBody()
	return sc
}

func (p *settingsPanel) fillBody() {
	p.body.RemoveAll()
	p.body.Add(p.banner)
	p.body.Add(p.appearanceCard())
	p.body.Add(p.modelCard())
	p.body.Add(p.agentCard())
	p.body.Add(p.learningCard())
	p.body.Add(p.contextCard())
	p.body.Add(p.integrationsCard())
	p.body.Add(p.permissionsCard())
	p.body.Refresh()
}

func (p *settingsPanel) section(title, hint string, items ...*widget.FormItem) fyne.CanvasObject {
	form := widget.NewForm(items...)
	parts := []fyne.CanvasObject{sectionHeading(title)}
	if hint != "" {
		parts = append(parts, newThemedText(hint, colorMuted))
	}
	parts = append(parts, form)
	card := newSurface(colorRaised, container.New(&padLayout{l: spaceL, r: spaceL, t: spaceM, b: spaceM},
		container.New(&columnLayout{gap: spaceS}, parts...))).border(colorBorder, 1)
	return card
}

// --- field helpers ---------------------------------------------------------

// validated wires on-entry validation: the failure appears under the field
// (Fyne's form check icons are noisy when most values are valid).
func (p *settingsPanel) validated(key, label string, e *widget.Entry, validate func(string) error, apply func(string)) *widget.FormItem {
	errLbl := widget.NewLabel("")
	errLbl.Wrapping = fyne.TextWrapWord
	errLbl.Importance = widget.DangerImportance
	errLbl.Hide()
	p.controls[label] = e
	e.OnChanged = func(s string) {
		err := validate(s)
		p.setBad(key, err != nil)
		if err != nil {
			errLbl.SetText(err.Error())
			errLbl.Show()
		} else {
			errLbl.Hide()
			apply(s)
		}
		errLbl.Refresh()
		p.changed()
	}
	return widget.NewFormItem(label, container.NewVBox(e, errLbl))
}

func (p *settingsPanel) strField(key, label string, value *string, validate func(string) error) *widget.FormItem {
	e := widget.NewEntry()
	e.SetText(*value)
	check := func(s string) error {
		if validate != nil {
			return validate(s)
		}
		return nil
	}
	return p.validated(key, label, e, check, func(s string) { *value = s })
}

func (p *settingsPanel) secretField(key, label string, value *string) *widget.FormItem {
	e := widget.NewPasswordEntry()
	e.SetText(*value)
	p.controls[label] = e
	e.OnChanged = func(s string) {
		*value = s
		p.changed()
	}
	return widget.NewFormItem(label, e)
}

func (p *settingsPanel) intField(key, label string, value *int, min, max int) *widget.FormItem {
	e := widget.NewEntry()
	e.SetText(strconv.Itoa(*value))
	parse := func(s string) (int, error) {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return 0, fmt.Errorf("number required")
		}
		if n < min || n > max {
			return 0, fmt.Errorf("must be %d–%d", min, max)
		}
		return n, nil
	}
	return p.validated(key, label, e, func(s string) error { _, err := parse(s); return err },
		func(s string) { n, _ := parse(s); *value = n })
}

func (p *settingsPanel) floatField(key, label string, value *float64, min, max float64) *widget.FormItem {
	e := widget.NewEntry()
	e.SetText(strconv.FormatFloat(*value, 'g', -1, 64))
	parse := func(s string) (float64, error) {
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return 0, fmt.Errorf("number required")
		}
		if f < min || f > max {
			return 0, fmt.Errorf("must be %g–%g", min, max)
		}
		return f, nil
	}
	return p.validated(key, label, e, func(s string) error { _, err := parse(s); return err },
		func(s string) { f, _ := parse(s); *value = f })
}

// selectField binds a select whose display labels map 1:1 to values; use
// "default" as the display label for an empty value.
func (p *settingsPanel) selectField(label string, options []string, display func(string) string, value *string) *widget.FormItem {
	sel := widget.NewSelect(options, nil)
	sel.SetSelected(display(*value))
	p.controls[label] = sel
	sel.OnChanged = func(choice string) {
		for _, opt := range options {
			if display(opt) == choice {
				*value = opt
			}
		}
		p.changed()
	}
	return widget.NewFormItem(label, sel)
}

func (p *settingsPanel) checkField(label string, value *bool) *widget.FormItem {
	c := widget.NewCheck("", nil)
	c.SetChecked(*value)
	p.controls[label] = c
	c.OnChanged = func(b bool) {
		*value = b
		p.changed()
	}
	return widget.NewFormItem(label, c)
}

// --- sections --------------------------------------------------------------

func (p *settingsPanel) appearanceCard() fyne.CanvasObject {
	scaleLbl := newThemedText(fmt.Sprintf("%.0f%%", p.draft.UI.TextScale*100), colorMuted)
	slider := widget.NewSlider(config.MinTextScale*100, config.MaxTextScale*100)
	slider.Step = 5
	slider.SetValue(p.draft.UI.TextScale * 100)
	p.controls["Text size"] = slider
	slider.OnChanged = func(v float64) {
		scaleLbl.SetText(fmt.Sprintf("%.0f%%", v))
	}
	slider.OnChangeEnded = func(v float64) {
		p.s.setTextScale(v / 100)
		p.draft.UI.TextScale = p.s.a.Cfg.UI.TextScale
	}

	themeSel := widget.NewSelect([]string{"System", "Light", "Dark"}, nil)
	themeSel.SetSelected(themeLabel(p.draft.UI.ThemeMode()))
	p.controls["Theme"] = themeSel
	themeSel.OnChanged = func(choice string) {
		p.s.setThemeMode(strings.ToLower(choice))
		p.draft.UI.Theme = p.s.a.Cfg.UI.Theme
	}
	form := widget.NewForm(
		widget.NewFormItem("Theme", themeSel),
		widget.NewFormItem("Text size", container.NewBorder(nil, nil, nil, scaleLbl, slider)),
	)
	return p.section("Appearance",
		"Applies immediately and is saved to your configuration.", form.Items...)
}

func (p *settingsPanel) modelCard() fyne.CanvasObject {
	prov := &p.draft.Provider
	items := []*widget.FormItem{
		p.strField("endpoint", "Endpoint", &prov.Endpoint, func(s string) error {
			if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
				return fmt.Errorf("must start with http:// or https://")
			}
			return nil
		}),
		p.strField("model", "Model", &prov.Model, func(s string) error {
			if strings.TrimSpace(s) == "" {
				return fmt.Errorf("model is required")
			}
			return nil
		}),
		p.secretField("api_key", "API key", &prov.APIKey),
		p.intField("ctx_window", "Context window", &prov.ContextWindow, 0, 10_000_000),
		p.intField("max_tokens", "Max output tokens", &prov.MaxTokens, 1, 1_000_000),
		p.floatField("temperature", "Temperature", &prov.Temperature, 0, 2),
		p.selectField("Reasoning effort", []string{"", "low", "medium", "high"}, func(v string) string {
			if v == "" {
				return "default"
			}
			return v
		}, &prov.ReasoningEffort),
		p.intField("retries", "Max retries", &prov.MaxRetries, 0, 10),
		p.intField("first_event", "First-event timeout (s)", &prov.StreamFirstEventTimeoutS, 1, 3600),
		p.intField("idle_timeout", "Stream idle timeout (s)", &prov.StreamIdleTimeoutS, 1, 3600),
	}
	return p.section("Model", "OpenAI Chat-Completions endpoint. \"auto\" picks the first advertised model.", items...)
}

func (p *settingsPanel) agentCard() fyne.CanvasObject {
	ag := &p.draft.Agent
	modes := []string{"default", "plan", "acceptEdits", "bypassPermissions", "dontAsk"}
	items := []*widget.FormItem{
		p.selectField("Permission mode", modes, func(v string) string { return v }, &ag.PermissionMode),
		p.intField("iterations", "Tool iteration limit", &ag.ToolIterationLimit, 1, 1000),
		p.checkField("Enable subagents", &ag.Subagents),
		p.checkField("Enable the script tool", &ag.Script),
	}
	return p.section("Agent", "Permission rules themselves live under Permissions.", items...)
}

func (p *settingsPanel) learningCard() fyne.CanvasObject {
	ui := p.draft

	mem := []*widget.FormItem{
		p.checkField("Memory enabled", &ui.Memory.Enabled),
		p.checkField("Require approval for memory writes", &ui.Memory.WriteApproval),
		p.intField("mem_limit", "MEMORY.md character limit", &ui.Memory.MemoryCharLimit, 100, 100_000),
		p.intField("user_limit", "USER.md character limit", &ui.Memory.UserCharLimit, 100, 100_000),
	}
	skills := []*widget.FormItem{
		p.checkField("Skills enabled", &ui.Skills.Enabled),
		p.checkField("Require approval for skill writes", &ui.Skills.WriteApproval),
	}
	review := []*widget.FormItem{
		p.checkField("Background review enabled", &ui.Review.Enabled),
		p.checkField("Defer until idle", &ui.Review.DeferUntilIdle),
		p.checkField("Notify when lessons are saved", &ui.Review.Notify),
		p.intField("review_settle", "Idle settle (s)", &ui.Review.IdleSettleS, 0, 3600),
		p.intField("review_age", "Max session age (s)", &ui.Review.MaxAgeS, 60, 86400),
		p.intField("review_tokens", "Max input tokens", &ui.Review.MaxInputTokens, 1000, 1_000_000),
	}
	improve := []*widget.FormItem{
		p.checkField("Harness improvement enabled", &ui.Improve.Enabled),
		p.checkField("Automatic pass after idle", &ui.Improve.Auto),
		p.intField("improve_idle", "Auto pass idle delay (s)", &ui.Improve.AutoIdleS, 0, 3600),
		p.checkField("Keep an improvement library", &ui.Improve.Library),
		p.selectField("Improve mode", []string{"sequential", "parallel"}, func(v string) string { return v }, &ui.Improve.Mode),
		p.intField("improve_k", "Recursion depth (k)", &ui.Improve.K, 1, 5),
		p.intField("improve_wall", "Total wall clock (s)", &ui.Improve.TotalWallS, 0, 3600),
		p.intField("improve_tokens", "Max total tokens", &ui.Improve.MaxTotalTokens, 0, 100_000_000),
	}

	body := container.New(&columnLayout{gap: spaceS},
		subHeading("Memory"), widget.NewForm(mem...),
		subHeading("Skills"), widget.NewForm(skills...),
		subHeading("Review"), widget.NewForm(review...),
		subHeading("Improve"), widget.NewForm(improve...),
	)
	return newSurface(colorRaised, container.New(&padLayout{l: spaceL, r: spaceL, t: spaceM, b: spaceM},
		container.New(&columnLayout{gap: spaceS}, sectionHeading("Learning"), body))).border(colorBorder, 1)
}

func (p *settingsPanel) contextCard() fyne.CanvasObject {
	c := &p.draft.Compaction
	items := []*widget.FormItem{
		p.checkField("Compaction enabled", &c.Enabled),
		p.intField("compact_pct", "Threshold (% of context)", &c.ThresholdPct, 1, 95),
		p.intField("compact_turns", "Keep recent turns", &c.KeepRecentTurns, 1, 100),
	}
	return p.section("Context", "Compaction summarizes older turns before the context window fills.", items...)
}

func (p *settingsPanel) integrationsCard() fyne.CanvasObject {
	t := &p.draft.Tie
	m := &p.draft.MCP
	h := &p.draft.Harness
	items := []*widget.FormItem{
		p.checkField("tie index enabled", &t.Enabled),
		p.strField("tie_collection", "tie collection", &t.Collection, nil),
		p.strField("tie_config", "tie config name", &t.ConfigName, nil),
		p.checkField("MCP enabled", &m.Enabled),
		p.checkField("Auto-detect installed servers", &m.AutoDetect),
		p.checkField("Harness enabled", &h.Enabled),
		p.intField("harness_limit", "Harness character limit", &h.CharLimit, 0, 1_000_000),
	}

	servers := p.s.mcpServers
	list := container.New(&columnLayout{gap: 2})
	if len(servers) == 0 {
		list.Add(newThemedText("No MCP servers configured.", colorMuted))
	}
	for _, srv := range servers {
		state := "offline"
		if boolean(srv["connected"]) {
			state = fmt.Sprintf("online · %d tools", int(num(srv["tools"])))
		}
		list.Add(container.New(&rowLayout{gap: spaceS},
			newThemedText(str(srv["name"]), theme.ColorNameForeground),
			newThemedText(state, colorMuted)))
	}
	edit := widget.NewButton("Edit servers in JSON…", p.s.showSettingsJSON)
	edit.Importance = widget.LowImportance
	items = append(items, widget.NewFormItem("MCP servers",
		container.NewVBox(list, edit)))

	return p.section("Integrations", "tie, MCP, and harness toggles. Reconnects happen on restart.", items...)
}

func (p *settingsPanel) permissionsCard() fyne.CanvasObject {
	ui := p.draft
	lines := container.New(&columnLayout{gap: 2})
	addRules := func(title string, rules permission.Config) {
		if len(rules) == 0 {
			return
		}
		lines.Add(newThemedText(title, colorMuted))
		for _, r := range rules {
			lines.Add(newThemedText(fmt.Sprintf("  %s %s → %s", r.Action, r.Resource, r.Effect), theme.ColorNameForeground))
		}
	}
	addRules("Global", ruleList(ui.Permission))
	addRules("Permissions (array form)", ruleList(ui.Permissions))
	addRules("Agent overrides", ruleList(ui.Agent.Permission))
	legacy := func(name string, list []string) {
		if len(list) == 0 {
			return
		}
		lines.Add(newThemedText(fmt.Sprintf("Legacy %s: %s", name, strings.Join(list, ", ")), colorMuted))
	}
	legacy("allow", ui.Agent.Allow)
	legacy("ask", ui.Agent.Ask)
	legacy("deny", ui.Agent.Deny)

	edit := widget.NewButton("Edit rules in JSON…", p.s.showSettingsJSON)
	edit.Importance = widget.LowImportance
	return p.section("Permissions", "Read-only overview; last matching rule wins.", widget.NewFormItem("Rules",
		container.NewVBox(lines, vgap(spaceS), edit)))
}

// subHeading is a bold caption between groups inside a card.
func subHeading(text string) *themedText {
	t := newThemedText(text, theme.ColorNameForeground)
	t.bold = true
	return t
}

// themeLabel is the display label for a persisted theme mode.
func themeLabel(mode string) string {
	switch mode {
	case "light":
		return "Light"
	case "dark":
		return "Dark"
	default:
		return "System"
	}
}

// ruleList is a nil-safe permission.Config for the read-only view.
func ruleList(c permission.Config) permission.Config {
	if c == nil {
		return permission.Config{}
	}
	return c
}

// sectionHeading is a bold sub-heading inside pages and panels.
func sectionHeading(text string) *themedText {
	t := newThemedText(text, theme.ColorNameForeground)
	t.size = theme.SizeNameSubHeadingText
	t.bold = true
	return t
}
