package main

import (
	"context"
	"log"
	"os"

	"fyagent/assets"
	"fyagent/core/app"

	"github.com/uidbz/fynerisor/gui"
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
