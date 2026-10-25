package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"fyne.io/fyne/v2"
	coreapp "github.com/uidbz/anneal/core/app"
	"github.com/uidbz/anneal/core/uiapi"
	"github.com/uidbz/fynerisor/gui"
	"github.com/uidbz/fynerisor/gui/guithread"
)

// runScriptUI is the developer/e2e escape hatch: a full-window fynerisor host
// with the complete `agent` bridge API, driven by ANNEAL_UI_DIR/main.risor.
// It is not the product UI; it keeps the scripted e2e drivers and custom UI
// hacking working after the Go shell became the default.
func runScriptUI(a *coreapp.App, checkOnly bool) error {
	bridge := uiapi.New(a.Bus)
	api := a.AgentAPI()
	for name, fn := range bridge.API() {
		api[name] = fn // on + quit
	}

	dir := a.Cfg.UI.UIDir
	if dir == "" {
		dir = os.Getenv("ANNEAL_UI_DIR")
	}
	if dir == "" {
		return fmt.Errorf("script UI: set ANNEAL_UI_DIR to a directory containing main.risor")
	}
	src, err := os.ReadFile(dir + "/main.risor")
	if err != nil {
		return fmt.Errorf("script UI: %w", err)
	}

	fw := gui.NewApp("Anneal (script UI)",
		gui.WithAppName("anneal"),
		gui.WithJSON(),
		gui.WithStrings(),
		gui.WithGlobal("agent", api),
	)
	bridge.Attach(fw)
	fw.LoadScript(string(src))
	if err := fw.CompileScript(); err != nil {
		return fmt.Errorf("script UI compile: %w", err)
	}
	if checkOnly {
		fmt.Println("anneal: script UI compiles")
		return nil
	}

	fw.Execute()
	bridge.Ready()
	a.ReplayState()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)

	time.AfterFunc(500*time.Millisecond, func() {
		guithread.Do(func() { fw.FyneWindow.Resize(fyne.NewSize(1100, 750)) })
	})
	fw.ShowAndRun()
	return nil
}
