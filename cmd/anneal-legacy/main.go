package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/uidbz/anneal/assets"
	"github.com/uidbz/anneal/core/agent"
	"github.com/uidbz/anneal/core/app"

	"fyne.io/fyne/v2"
	"github.com/uidbz/fynerisor/gui"
	"github.com/uidbz/fynerisor/gui/guithread"
)

func main() {
	rebuild := flag.Bool("rebuild-index", false, "re-index all sessions into tie and exit")
	checkUI := flag.Bool("check-ui", false, "compile the UI script and exit")
	checkHarness := flag.Bool("check-harness", false, "validate harness components and exit")
	flag.Parse()

	a, err := app.New()
	if err != nil {
		log.Fatalf("anneal: %v", err)
	}

	if *checkHarness {
		fmt.Println("anneal:", agent.FormatHarnessInfo(a.Runner.HarnessInfo()))
		return
	}

	if *rebuild {
		if err := a.Tie.Connect(); err != nil {
			log.Fatalf("anneal: tie connect: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		n, err := a.Runner.RebuildIndex(ctx)
		if err != nil {
			log.Fatalf("anneal: rebuild: %v", err)
		}
		fmt.Printf("anneal: re-indexed %d sessions\n", n)
		return
	}

	fw := gui.NewApp("Anneal (legacy)",
		gui.WithAppName("anneal"),
		gui.WithJSON(),
		gui.WithStrings(),
		gui.WithGlobal("agent", a.AgentAPI()),
	)
	a.Bridge.Attach(fw)

	src, err := uiSource(a.Cfg.UI.UIDir)
	if err != nil {
		log.Fatalf("anneal: UI missing: %v", err)
	}
	fw.LoadScript(src)
	if err := fw.CompileScript(); err != nil {
		log.Fatalf("anneal: UI compile: %v", err)
	}
	if *checkUI {
		fmt.Println("anneal: UI compiles")
		return
	}
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
		dir = os.Getenv("ANNEAL_UI_DIR")
	}
	if dir == "" {
		dir = os.Getenv("FYAGENT_UI_DIR") // deprecated alias
	}
	if dir != "" {
		if b, err := os.ReadFile(dir + "/main.risor"); err == nil {
			return string(b), nil
		}
	}
	b, err := assets.UI.ReadFile("ui/main.risor")
	return string(b), err
}
