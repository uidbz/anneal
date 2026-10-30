// Command anneal is the hybrid desktop shell: the stable, high-traffic UI is
// plain Go/Fyne, while experimental surfaces (the harness panel first) are
// Risor scripts embedded as gui.CanvasObject widgets.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"github.com/uidbz/anneal/assets"
	"github.com/uidbz/anneal/core/agent"
	coreapp "github.com/uidbz/anneal/core/app"
)

func main() {
	rebuild := flag.Bool("rebuild-index", false, "re-index all sessions into tie and exit")
	checkUI := flag.Bool("check-ui", false, "compile the scripted pane and exit")
	checkHarness := flag.Bool("check-harness", false, "validate harness components and exit")
	scriptUI := flag.Bool("script-ui", false, "run a full-window scripted UI from ANNEAL_UI_DIR (e2e/dev)")
	checkScriptUI := flag.Bool("check-script-ui", false, "compile the ANNEAL_UI_DIR scripted UI and exit")
	sendTask := flag.String("send", "", "developer: auto-send this task after boot")
	quitAfterRun := flag.Bool("quit-after-run", false, "developer: quit after the auto-sent run finishes")
	flag.Parse()

	a, err := coreapp.New()
	if err != nil {
		log.Fatalf("anneal: %v", err)
	}
	paneSrc, err := paneSource(a.Cfg.UI.PaneDir)
	if err != nil {
		log.Fatalf("anneal: pane missing: %v", err)
	}
	if *scriptUI || *checkScriptUI {
		if err := runScriptUI(a, *checkScriptUI); err != nil {
			log.Fatalf("anneal: %v", err)
		}
		return
	}
	if *checkUI {
		if _, err := newScriptPane(nil, paneSrc,
			paneCapabilities{Events: true}, a.Bus, a.HarnessAPI(), paneEvent); err != nil {
			log.Fatalf("anneal: pane compile: %v", err)
		}
		fmt.Println("anneal: pane compiles")
		return
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

	fyneApp := fyneapp.New()
	win := fyneApp.NewWindow("Anneal")
	sh, err := newShell(a, fyneApp, win, paneSrc)
	if err != nil {
		log.Fatalf("anneal: UI: %v", err)
	}
	sh.autoSend = *sendTask
	sh.autoQuit = *quitAfterRun
	win.SetContent(sh.root)
	win.Resize(fyne.NewSize(1280, 800))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sh.start(ctx)
	win.ShowAndRun()
}

// paneSource prefers an on-disk pane script (config `ui.pane_dir`, then the
// full script-UI dir for co-located development) and falls back to the
// embedded copy.
func paneSource(configDir string) (string, error) {
	dirs := []string{
		configDir,
		os.Getenv("ANNEAL_PANE_DIR"),
		os.Getenv("ANNEAL_UI_DIR"),
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if b, err := os.ReadFile(dir + "/harness.risor"); err == nil {
			return string(b), nil
		}
	}
	b, err := assets.UI.ReadFile("ui/harness.risor")
	return string(b), err
}
