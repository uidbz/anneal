package main

import (
	"context"
	"log"
	"os"
	"time"

	"fyagent/assets"
	"fyagent/core/app"

	"fyne.io/fyne/v2"
	"github.com/uidbz/fynerisor/gui"
	"github.com/uidbz/fynerisor/gui/guithread"
)

func main() {
	a, err := app.New()
	if err != nil {
		log.Fatalf("fyagent: %v", err)
	}

	fw := gui.NewApp("fyagent",
		gui.WithAppName("fyagent"),
		gui.WithJSON(),
		gui.WithStrings(),
		gui.WithGlobal("agent", a.AgentAPI()),
	)
	a.Bridge.Attach(fw)

	src, err := uiSource(a.Cfg.UI.UIDir)
	if err != nil {
		log.Fatalf("fyagent: UI missing: %v", err)
	}
	fw.LoadScript(src)
	fw.Execute()
	a.Bridge.Ready() // core events may flow from here (fynerisor channel exists)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)

	// GLFW/Wayland ignores pre-show Resize hints; force the size once the
	// main loop is live.
	time.AfterFunc(500*time.Millisecond, func() {
		guithread.Do(func() { fw.FyneWindow.Resize(fyne.NewSize(1100, 750)) })
	})

	fw.ShowAndRun()
}

// uiSource prefers an on-disk UI (hot-reload during development) and falls
// back to the embedded copy.
func uiSource(dir string) (string, error) {
	if dir == "" {
		dir = os.Getenv("FYAGENT_UI_DIR")
	}
	if dir != "" {
		if b, err := os.ReadFile(dir + "/main.risor"); err == nil {
			return string(b), nil
		}
	}
	b, err := assets.UI.ReadFile("ui/main.risor")
	return string(b), err
}
