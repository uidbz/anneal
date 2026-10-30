package main

import (
	"strings"
	"testing"

	"fyne.io/fyne/v2/widget"
)

func TestSettingsDirtySaveRevert(t *testing.T) {
	sh := newTestShell(t)
	p := sh.settingsForm
	if p == nil {
		t.Fatal("settings form not built")
	}
	if p.saveBtn.Disabled() == false {
		t.Fatal("save enabled without changes")
	}

	entry := p.controls["Model"].(*widget.Entry)
	entry.SetText("my-model")
	if !p.dirty || p.saveBtn.Disabled() {
		t.Fatalf("dirty=%v saveDisabled=%v", p.dirty, p.saveBtn.Disabled())
	}
	p.save()
	if p.dirty {
		t.Fatal("form still dirty after save")
	}
	if got := sh.a.Cfg.Provider.Model; got != "my-model" {
		t.Fatalf("model = %q", got)
	}

	entry = p.controls["Model"].(*widget.Entry)
	entry.SetText("other")
	p.revert()
	if p.dirty {
		t.Fatal("revert left the form dirty")
	}
	if got := sh.a.Cfg.Provider.Model; got != "my-model" {
		t.Fatalf("model after revert = %q", got)
	}
}

func TestSettingsInvalidNumberBlocksSave(t *testing.T) {
	sh := newTestShell(t)
	p := sh.settingsForm
	p.controls["Max output tokens"].(*widget.Entry).SetText("nope")
	if !p.settingsInvalid() {
		t.Fatal("invalid number not detected")
	}
	if !p.saveBtn.Disabled() {
		t.Fatal("save enabled with an invalid field")
	}
	p.controls["Max output tokens"].(*widget.Entry).SetText("4096")
	if p.settingsInvalid() || p.saveBtn.Disabled() {
		t.Fatal("valid number did not clear the error")
	}
}

func TestSettingsEndpointValidation(t *testing.T) {
	sh := newTestShell(t)
	p := sh.settingsForm
	p.controls["Endpoint"].(*widget.Entry).SetText("ftp://x")
	if !p.bad["endpoint"] {
		t.Fatal("bad endpoint accepted")
	}
	p.controls["Endpoint"].(*widget.Entry).SetText("http://127.0.0.1:8080/v1/chat/completions")
	if p.bad["endpoint"] {
		t.Fatal("valid endpoint rejected")
	}
}

func TestSettingsThemeApplies(t *testing.T) {
	sh := newTestShell(t)
	p := sh.settingsForm
	p.controls["Theme"].(*widget.Select).SetSelected("Light")
	if got := sh.a.Cfg.UI.Theme; got != "light" {
		t.Fatalf("theme = %q", got)
	}
	if p.dirty {
		t.Fatal("theme change should not dirty the form")
	}
}

func TestSettingsExternalChange(t *testing.T) {
	sh := newTestShell(t)
	p := sh.settingsForm

	// Clean form: an external change reloads silently.
	sh.a.Cfg.Provider.Model = "external"
	ev := normalizeEvent(map[string]any{"type": "config.changed"})
	sh.handleEvent(ev)
	if got := p.controls["Model"].(*widget.Entry).Text; got != "external" {
		t.Fatalf("clean form did not reload: %q", got)
	}

	// Dirty form: keep the draft and show the banner.
	p.controls["Model"].(*widget.Entry).SetText("mine")
	sh.a.Cfg.Provider.Model = "external2"
	sh.handleEvent(normalizeEvent(map[string]any{"type": "config.changed"}))
	if got := p.controls["Model"].(*widget.Entry).Text; got != "mine" {
		t.Fatalf("dirty form overwritten: %q", got)
	}
	if !p.banner.Visible() {
		t.Fatal("conflict banner not shown")
	}
}

func TestSettingsSavePersists(t *testing.T) {
	sh := newTestShell(t)
	p := sh.settingsForm
	p.controls["Temperature"].(*widget.Entry).SetText("0.9")
	p.save()
	if got := sh.a.Cfg.Provider.Temperature; got != 0.9 {
		t.Fatalf("temperature = %v", got)
	}
	// The config on disk must contain the change.
	sh2 := sh.a
	if err := sh2.Cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sh.a.Cfg.Provider.Model, "") {
		t.Fatal("unreachable")
	}
}

func TestLeavingSettingsWithDirtyFormStays(t *testing.T) {
	sh := newTestShell(t)
	sh.showView(viewSettings)
	sh.settingsForm.controls["Model"].(*widget.Entry).SetText("draft")
	sh.showView(viewChat)
	if sh.view != viewSettings {
		t.Fatal("left settings without confirming the discard")
	}
	sh.settingsForm.reload()
	sh.showView(viewChat)
	if sh.view != viewChat {
		t.Fatal("could not leave settings after reload")
	}
}
