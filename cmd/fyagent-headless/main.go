// fyagent-headless: the fyagent core without a GUI. Same config, sessions,
// tie index, skills/memory/review and MCP — driven from a terminal.
//
//	fyagent-headless -p "fix the failing test"     # one-shot
//	fyagent-headless -i                            # REPL
//
// Output is human-readable text; -json emits the raw event stream (JSONL) —
// every event the GUI receives, machine-readable.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"fyagent/core/agent"
	"fyagent/core/app"
)

type runResult struct {
	ok    bool
	err   string
	usage string
}

type sink struct {
	jsonMode bool
	verbose  bool
	mu       sync.Mutex
	streamed bool
	done     chan runResult
	learn    chan struct{}
	app      *app.App
	allow    map[string]bool
	denyAll  bool
}

func (s *sink) handle(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	if s.jsonMode {
		fmt.Println(string(data))
	}
	var e map[string]any
	if json.Unmarshal(data, &e) != nil {
		return
	}
	t, _ := e["type"].(string)

	switch t {
	case "perm.request":
		s.decidePerm(e)
	case "text.delta":
		if !s.jsonMode {
			fmt.Print(e["text"])
			s.mu.Lock()
			s.streamed = true
			s.mu.Unlock()
		}
	case "message.final":
		s.mu.Lock()
		streamed := s.streamed
		s.streamed = false
		s.mu.Unlock()
		if !s.jsonMode {
			if streamed {
				fmt.Println()
			} else {
				fmt.Println(e["text"])
			}
		}
	case "tool.start":
		if !s.jsonMode {
			fmt.Printf("\n⚙ %s %s\n", e["name"], clip(fmt.Sprint(e["input"]), 120))
		}
	case "tool.result":
		if !s.jsonMode {
			mark := "✔"
			if b, _ := e["error"].(bool); b {
				mark = "✖"
			}
			fmt.Printf("  %s %s: %s\n", mark, e["name"], clip(fmt.Sprint(e["preview"]), 160))
		}
	case "run.finished":
		res := runResult{}
		if b, _ := e["ok"].(bool); b {
			res.ok = true
		}
		res.err = fmt.Sprint(e["error"])
		if u, ok := e["usage"].(map[string]any); ok {
			res.usage = fmt.Sprintf("in=%v out=%v ctx=%v%%", u["tin"], u["tout"], e["ctx_pct"])
		}
		if !s.jsonMode {
			if res.ok {
				fmt.Printf("── done (%s)\n", res.usage)
			} else {
				fmt.Printf("── error: %s\n", res.err)
			}
		}
		select {
		case s.done <- res:
		default:
		}
	case "review.done":
		if !s.jsonMode {
			if l, ok := e["ledger"].([]any); ok && len(l) > 0 {
				fmt.Printf("🧠 review saved %d lessons\n", len(l))
			} else {
				fmt.Println("🧠 review: nothing worth saving")
			}
		}
		select {
		case s.learn <- struct{}{}:
		default:
		}
	default:
		if s.verbose && !s.jsonMode {
			fmt.Fprintf(os.Stderr, "[%s] %s\n", t, strings.TrimSpace(string(data)))
		}
	}
}

func (s *sink) decidePerm(e map[string]any) {
	id := fmt.Sprint(e["id"])
	tool := fmt.Sprint(e["tool"])
	action := "deny"
	switch {
	case s.denyAll:
		action = "deny"
	case s.allow["*"], s.allow[tool]:
		action = "allow"
	}
	if !s.jsonMode {
		fmt.Printf("🔐 %s → %s (headless policy)\n", tool, action)
	}
	// respondPerm re-enters Emit; keep off the tap's lock path
	go s.app.Runner.RespondPerm(id, action)
}

func clip(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func main() {
	prompt := flag.String("p", "", "single task; print and exit")
	interactive := flag.Bool("i", false, "interactive REPL")
	jsonMode := flag.Bool("json", false, "emit raw event stream as JSONL")
	verbose := flag.Bool("v", false, "log all events to stderr")
	yolo := flag.Bool("yolo", false, "bypass all permission prompts")
	allowList := flag.String("allow", "", "comma-separated tool names to auto-allow")
	newSess := flag.Bool("new", false, "start a fresh session")
	session := flag.String("session", "", "resume a session by id")
	learn := flag.Bool("learn", false, "run the self-improvement review pass after the task")
	flag.Parse()

	if *prompt == "" && !*interactive {
		fmt.Fprintln(os.Stderr, `fyagent-headless: give -p "task" or -i (REPL); -h for flags`)
		os.Exit(2)
	}

	a, err := app.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fyagent-headless:", err)
		os.Exit(1)
	}

	s := &sink{jsonMode: *jsonMode, verbose: *verbose,
		done: make(chan runResult, 1), learn: make(chan struct{}, 1),
		app: a, denyAll: !*yolo && *allowList == "", allow: map[string]bool{}}
	for _, t := range strings.Split(*allowList, ",") {
		if t != "" {
			s.allow[t] = true
		}
	}
	if *yolo {
		s.allow["*"] = true
		a.Runner.SetMode("bypassPermissions")
	}
	a.Bridge.SetTap(func(v any) { go s.handle(v) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)
	a.ConnectTie()    // blocking is fine headless
	a.ConnectMCP(ctx) // blocking is fine headless

	if *newSess {
		a.Runner.NewSession()
	} else if *session != "" {
		a.Runner.OpenSession(*session)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		if a.Runner.Busy() {
			a.Runner.Interrupt()
		} else {
			cancel()
			os.Exit(130)
		}
		<-sig // second signal: hard exit
		cancel()
		os.Exit(130)
	}()

	runTask := func(task string) int {
		a.Runner.Send(task)
		select {
		case res := <-s.done:
			if *learn {
				a.Review.LearnNow()
				select {
				case <-s.learn:
				case <-time.After(3 * time.Minute):
					fmt.Fprintln(os.Stderr, "fyagent-headless: review pass timed out")
				}
			}
			if res.ok {
				return 0
			}
			return 1
		case <-ctx.Done():
			return 130
		case <-time.After(60 * time.Minute):
			a.Runner.Interrupt()
			return 124
		}
	}

	code := 0
	if *prompt != "" {
		code = runTask(*prompt)
	} else {
		sc := bufio.NewScanner(os.Stdin)
		fmt.Fprintln(os.Stderr, `fyagent-headless REPL — type a task (Ctrl-D to quit, /new /interrupt supported)`)
		for {
			fmt.Print("> ")
			if !sc.Scan() {
				break
			}
			line := strings.TrimSpace(sc.Text())
			switch line {
			case "":
				continue
			case "/quit", "/exit":
				os.Exit(code)
			case "/new":
				a.Runner.NewSession()
				fmt.Fprintln(os.Stderr, "(new session)")
				continue
			case "/interrupt":
				a.Runner.Interrupt()
				continue
			case "/harness":
				fmt.Fprintln(os.Stderr, agent.FormatHarnessInfo(a.Runner.HarnessInfo()))
				continue
			}
			code = runTask(line)
		}
	}
	os.Exit(code)
}
