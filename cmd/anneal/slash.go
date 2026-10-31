package main

import (
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
		s.showText("commands", "/new /sessions /plan /build /stop /textsize /memory /skills /journey /activity /runs /index /learn /improve /harness /backend /quit")
	case "/new":
		s.b.newSession()
	case "/sessions":
		s.showSessions()
	case "/plan":
		s.b.setMode("plan")
	case "/build":
		s.b.setMode("default")
	case "/stop", "/interrupt":
		s.b.interrupt()
	case "/textsize":
		s.textSizeCommand(fields[1:])
	case "/theme":
		if len(fields) < 2 {
			s.notice(noticeInfo, "Theme: "+s.cfg.UI.ThemeMode()+" (system|light|dark)")
			return
		}
		s.setThemeMode(fields[1])
	case "/cwd", "/pwd":
		s.notice(noticeInfo, "Working directory: "+s.b.cwd())
	case "/backend":
		s.backendCommand(fields[1:])
	case "/memory":
		view, err := s.b.memoryView()
		if err != nil {
			s.addNotice("⚠ memory: " + err.Error())
			return
		}
		m, u := str(view["memory"]), str(view["user"])
		s.showText("memory", fmt.Sprintf("MEMORY.md (%d/%d):\n%s\n\nUSER.md (%d/%d):\n%s",
			len(m), int(num(view["memory_cap"])), m,
			len(u), int(num(view["user_cap"])), u))
	case "/skills":
		var b strings.Builder
		list, err := s.b.skillsList()
		if err != nil {
			s.addNotice("⚠ skills: " + err.Error())
			return
		}
		if len(list) == 0 {
			b.WriteString("skills: none installed")
		}
		for _, sk := range list {
			fmt.Fprintf(&b, "- %s: %s\n", sk.Name, sk.Description)
		}
		s.showText("skills", b.String())
	case "/journey":
		entries, err := s.b.journeyTail(25)
		if err != nil {
			s.addNotice("⚠ journey: " + err.Error())
			return
		}
		var b strings.Builder
		for _, e := range entries {
			fmt.Fprintf(&b, "%s  %s\n", e.TS, e.Kind)
		}
		s.showText("journey", b.String())
	case "/activity", "/runs":
		s.refreshActivity()
		s.showView(viewActivity)
	case "/index":
		s.b.rebuildIndex()
		s.addNotice("index rebuild requested")
	case "/learn":
		s.b.learnNow()
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

// backendCommand implements /backend:
//
//	/backend            show the active backend and the registry
//	/backend <name|addr>  choose the backend for the next launch
//
// Switching is a restart boundary, not a hot swap: the whole session view
// (transcript, sidebar, pane bridge) belongs to the current backend, and
// one-instance-per-project is the tenancy model (docs/USING.md).
func (s *shell) backendCommand(args []string) {
	cur := s.b.describe()
	if len(args) == 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "backend: %s\nworkdir: %s\n", cur, s.b.cwd())
		if len(s.cfg.UI.Backends) == 0 {
			b.WriteString("registry: empty (add ui.backends entries in config.json)")
		} else {
			b.WriteString("registry:")
			for _, be := range s.cfg.UI.Backends {
				marker := " "
				if be.Name == cur || be.Addr == cur {
					marker = "*"
				}
				addr := be.Addr
				if addr == "" {
					addr = "embedded core"
				}
				fmt.Fprintf(&b, "\n %s %s  %s", marker, be.Name, addr)
			}
		}
		s.showText("backend", b.String())
		return
	}
	want := args[0]
	found := false
	for _, be := range s.cfg.UI.Backends {
		if be.Name == want || be.Addr == want {
			s.cfg.UI.Backend = be.Addr
			found = true
			break
		}
	}
	if !found {
		if want == "local" {
			s.cfg.UI.Backend = ""
			found = true
		} else if strings.Contains(want, ":") || strings.HasPrefix(want, "unix:") {
			s.cfg.UI.Backend = want
			found = true
		}
	}
	if !found {
		s.addNotice("⚠ unknown backend " + want + " — try /backend")
		return
	}
	if err := s.cfg.Save(); err != nil {
		s.addNotice("⚠ backend save: " + err.Error())
		return
	}
	next := "embedded core"
	if s.cfg.UI.Backend != "" {
		next = s.cfg.UI.Backend
	}
	s.addNotice("backend for next launch: " + next + " — restart anneal to switch")
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
			s.cfg.UI.TextScale*100, config.MinTextScale*100, config.MaxTextScale*100))
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
			scale += s.cfg.UI.TextScale
		}
	case relative:
		scale = s.cfg.UI.TextScale + v
	}
	s.setTextScale(scale)
}
