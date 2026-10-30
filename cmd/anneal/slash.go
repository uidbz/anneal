package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/uidbz/anneal/core/config"
)

func (s *shell) slash(line string) {
	fields := strings.Fields(line)
	cmd := fields[0]
	switch cmd {
	case "/help":
		s.showText("commands", "/new /sessions /plan /build /stop /textsize /memory /skills /journey /activity /runs /index /learn /improve /harness /quit")
	case "/new":
		s.a.Runner.NewSession()
	case "/sessions":
		s.showSessions()
	case "/plan":
		s.a.Runner.SetMode("plan")
	case "/build":
		s.a.Runner.SetMode("default")
	case "/stop", "/interrupt":
		s.a.Runner.Interrupt()
	case "/textsize":
		s.textSizeCommand(fields[1:])
	case "/memory":
		m, _ := s.a.Mem.Read("memory")
		u, _ := s.a.Mem.Read("user")
		s.showText("memory", fmt.Sprintf("MEMORY.md (%d/%d):\n%s\n\nUSER.md (%d/%d):\n%s",
			len(m), s.a.Cfg.Memory.MemoryCharLimit, m,
			len(u), s.a.Cfg.Memory.UserCharLimit, u))
	case "/skills":
		var b strings.Builder
		list := s.a.Skills.List()
		if len(list) == 0 {
			b.WriteString("skills: none installed")
		}
		for _, sk := range list {
			fmt.Fprintf(&b, "- %s: %s\n", sk.Name, sk.Description)
		}
		s.showText("skills", b.String())
	case "/journey":
		var b strings.Builder
		for _, e := range s.a.Journey.Tail(25) {
			fmt.Fprintf(&b, "%s  %s\n", e.TS, e.Kind)
		}
		s.showText("journey", b.String())
	case "/activity", "/runs":
		s.refreshActivity()
		s.showView(viewActivity)
	case "/index":
		go func() {
			n, err := s.a.Runner.RebuildIndex(context.Background())
			msg := map[string]any{"type": "tie.rebuilt", "sessions": n, "error": ""}
			if err != nil {
				msg["error"] = err.Error()
			}
			s.a.Bus.Emit(msg)
		}()
		s.addNotice("index rebuild requested")
	case "/learn":
		s.a.Review.LearnNow()
		s.addNotice("learning pass queued")
	case "/improve":
		s.improveNow()
	case "/harness":
		s.showView(viewHarness)
	case "/quit":
		s.app.Quit()
	default:
		s.addNotice("unknown command " + cmd + " — try /help")
	}
}

// textSizeCommand implements /textsize:
//
//	/textsize             show the current size
//	/textsize 120%        absolute percentage
//	/textsize +10%        relative percentage points
//	/textsize 1.2         absolute multiplier
//	/textsize reset       back to 100%
func (s *shell) textSizeCommand(args []string) {
	if len(args) == 0 {
		s.addNotice(fmt.Sprintf("text size: %.0f%% (range %.0f–%.0f%%)",
			s.a.Cfg.UI.TextScale*100, config.MinTextScale*100, config.MaxTextScale*100))
		return
	}
	arg := strings.TrimSpace(strings.ToLower(args[0]))
	switch arg {
	case "reset", "default", "normal":
		s.setTextScale(1.0)
		return
	}
	percent := strings.HasSuffix(arg, "%")
	raw := strings.TrimSuffix(arg, "%")
	relative := strings.HasPrefix(raw, "+") || strings.HasPrefix(raw, "-")
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		s.addNotice("⚠ /textsize expects e.g. 120%, +10%, 1.2, +0.1, or reset")
		return
	}
	scale := v
	switch {
	case percent:
		scale = v / 100
		if relative {
			scale += s.a.Cfg.UI.TextScale
		}
	case relative:
		scale = s.a.Cfg.UI.TextScale + v
	}
	s.setTextScale(scale)
}
