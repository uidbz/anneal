package main

import (
	"log"
	"os"

	"fyagent/assets"
	"fyagent/core/uiapi"

	"github.com/uidbz/fynerisor/gui"
)

func main() {
	bridge := uiapi.New()

	fw := gui.NewApp("fyagent",
		gui.WithAppName("fyagent"),
		gui.WithJSON(),
		gui.WithStrings(),
		gui.WithGlobal("agent", bridge.API()),
	)
	bridge.Attach(fw)

	src, err := uiSource()
	if err != nil {
		log.Fatalf("fyagent: UI missing: %v", err)
	}
	fw.LoadScript(src)
	fw.Execute()
	fw.ShowAndRun()
}

// uiSource prefers an on-disk UI (hot-reload during development) and falls
// back to the embedded copy.
func uiSource() (string, error) {
	if dir := os.Getenv("FYAGENT_UI_DIR"); dir != "" {
		if b, err := os.ReadFile(dir + "/main.risor"); err == nil {
			return string(b), nil
		}
	}
	b, err := assets.UI.ReadFile("ui/main.risor")
	return string(b), err
}
