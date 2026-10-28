// anneal-headless: the anneal core without a GUI. Same config, sessions,
// tie index, skills/memory/review and MCP — driven from a terminal.
//
//	anneal-headless -p "fix the failing test"     # one-shot
//	anneal-headless -i                            # REPL
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

	"github.com/uidbz/anneal/core/agent"
	"github.com/uidbz/anneal/core/app"
	"github.com/uidbz/anneal/core/improve"
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

	// run correlation: after a timeout/interrupt the aborted run may still
	// finish; its events are dropped instead of printing or satisfying the
	// next task's wait.
	await int
	stale map[int]bool
}

func runID(e map[string]any) (int, bool) {
	switch v := e["run"].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	default:
		return 0, false
	}
}

// acceptRun filters run-scoped events by run id. Events without a numeric
// run id (provider/review/improve, where "run" is a string or absent) pass.
func (s *sink) acceptRun(e map[string]any) bool {
	id, ok := runID(e)
	if !ok {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stale[id] {
		// Drop everything the aborted run still emits; its finish is last and
		// releases the tombstone.
		if e["type"] == "run.finished" {
			delete(s.stale, id)
		}
		return false
	}
	return s.await == 0 || id == s.await
}

func (s *sink) setAwait(id int) {
	s.mu.Lock()
	s.await = id
	s.mu.Unlock()
}

func (s *sink) discardRun(id int) {
	if id == 0 {
		return
	}
	s.mu.Lock()
	if s.stale == nil {
		s.stale = map[int]bool{}
	}
	s.stale[id] = true
	s.await = 0
	s.mu.Unlock()
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
	if !s.acceptRun(e) {
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
	case "review.flag":
		if !s.jsonMode {
			fmt.Printf("🪧 review flagged a harness gap: %v\n", e["gap"])
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
	case "improve.budget":
		if !s.jsonMode {
			fmt.Printf("🔧 improve: budget reached (%v tokens ≥ %v) at iter %v\n", e["spent_tokens"], e["budget"], e["iter"])
		}
	case "improve.selector":
		if !s.jsonMode {
			fmt.Printf("🔧 improve: selector chose iter %v (%v: %v)\n", e["chosen_iter"], e["label"], e["reason"])
		}
	case "improve.library":
		if s.jsonMode {
			return
		}
		if toStr := fmt.Sprint(e["entry"]); toStr != "" && toStr != "<nil>" {
			fmt.Printf("🔧 improve: library entry %s\n", toStr)
		} else {
			fmt.Printf("🔧 improve: library entry failed: %v\n", e["error"])
		}
	case "improve.done":
		if s.jsonMode {
			return
		}
		if e["ok"] == true {
			if e["successor"] == true {
				fmt.Println("🔧 improve: successor harness composed (session overlay applied)")
			} else {
				fmt.Println("🔧 improve: composer kept the current harness")
			}
		} else {
			fmt.Printf("🔧 improve failed: %v\n", e["error"])
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
	action := fmt.Sprint(e["action"])
	if action == "" {
		action = tool
	}
	answer := "deny"
	switch {
	case s.denyAll:
		answer = "deny"
	case s.allow["*"], s.allow[action], s.allow[tool]:
		answer = "allow"
	}
	if !s.jsonMode {
		fmt.Printf("🔐 %s → %s (headless policy)\n", action, answer)
	}
	// respondPerm re-enters Emit; keep off the tap's lock path
	go s.app.Runner.RespondPerm(id, answer)
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
	allowList := flag.String("allow", "", "comma-separated permission actions or tool names to auto-allow")
	newSess := flag.Bool("new", false, "start a fresh session")
	session := flag.String("session", "", "resume a session by id")
	learn := flag.Bool("learn", false, "run the self-improvement review pass after the task")
	improveFlag := flag.Bool("improve", false, "after the task, run one contrastive harness-improvement step (propose→probe→compose)")
	improveK := flag.Int("k", 0, "recursion depth for -improve (0 = config, default 1)")
	improveMode := flag.String("improve-mode", "", "improve recursion mode: sequential (default) | parallel")
	improveResume := flag.String("improve-resume", "", "resume a prior improve run by id, skipping completed iterations")
	flag.Parse()

	if *prompt == "" && !*interactive {
		fmt.Fprintln(os.Stderr, `anneal-headless: give -p "task" or -i (REPL); -h for flags`)
		os.Exit(2)
	}

	a, err := app.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "anneal-headless:", err)
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
	// One ordered consumer: a goroutine per event would reorder text deltas
	// and could deliver run.finished before message.final/tool.result.
	events := make(chan any, 1024)
	a.Bus.AddTap(func(v any) { events <- v })
	go func() {
		for v := range events {
			s.handle(v)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.StartBlocking(ctx) // monitor + review async; tie/MCP connected inline

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
		id, ok := a.Runner.StartTask(task)
		if !ok {
			fmt.Fprintln(os.Stderr, "anneal-headless: a run is already active")
			return 1
		}
		s.setAwait(id)
		select {
		case res := <-s.done:
			s.setAwait(0)
			if *learn {
				a.Review.LearnNow()
				select {
				case <-s.learn:
				case <-time.After(3 * time.Minute):
					fmt.Fprintln(os.Stderr, "anneal-headless: review pass timed out")
				}
			}
			code := 0
			if !res.ok {
				code = 1
			}
			if *improveFlag && res.ok {
				fmt.Fprintln(os.Stderr, "🔧 harness improvement: propose → probe → compose …")
				ictx, icancel := context.WithTimeout(context.Background(), 25*time.Minute)
				defer icancel()
				ires, ierr := a.ImproveWith(ictx, *improveK, *improveMode, *improveResume)
				if ierr != nil {
					fmt.Fprintf(os.Stderr, "anneal-headless: improve: %v\n", ierr)
				} else {
					printImprove(ires)
				}
			}
			return code
		case <-ctx.Done():
			s.discardRun(id)
			return 130
		case <-time.After(60 * time.Minute):
			s.discardRun(id)
			a.Runner.Interrupt()
			return 124
		}
	}

	code := 0
	if *prompt != "" {
		code = runTask(*prompt)
	} else {
		sc := bufio.NewScanner(os.Stdin)
		fmt.Fprintln(os.Stderr, `anneal-headless REPL — type a task (Ctrl-D to quit, /new /interrupt supported)`)
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
			}
			if strings.HasPrefix(line, "/harness") {
				fields := strings.Fields(line)
				switch {
				case len(fields) >= 3 && fields[1] == "promote":
					backup, perr := a.Libs.Promote(fields[2])
					if perr != nil {
						fmt.Fprintf(os.Stderr, "promote: %v\n", perr)
					} else {
						msg := "promoted into the global harness"
						if backup != "" {
							msg += " (previous files backed up at " + backup + ")"
						}
						fmt.Fprintf(os.Stderr, "harness %s: %s — restart anneal to re-read guardrail RULES\n", fields[2], msg)
					}
				case len(fields) >= 2 && fields[1] == "list":
					ls := a.Libs.List()
					if len(ls) == 0 {
						fmt.Fprintln(os.Stderr, "library: empty")
					}
					for _, e := range ls {
						fmt.Fprintf(os.Stderr, "  %s — %s [%s]\n", e.ID, e.Situation, e.Components)
					}
				default:
					fmt.Fprintln(os.Stderr, agent.FormatHarnessInfo(a.Runner.HarnessInfo()))
					if fs := a.Review.PendingFlags(5); len(fs) > 0 {
						fmt.Fprintf(os.Stderr, "reviewer flags (feed into the next /improve):\n")
						for _, f := range fs {
							fmt.Fprintf(os.Stderr, "  🪧 %s\n", f.Gap)
						}
					}
				}
				continue
			}
			if strings.HasPrefix(line, "/improve") {
				fmt.Fprintln(os.Stderr, "🔧 harness improvement: propose → probe → compose …")
				ictx, icancel := context.WithTimeout(context.Background(), 25*time.Minute)
				ires, ierr := a.ImproveWith(ictx, *improveK, *improveMode, *improveResume)
				icancel()
				if ierr != nil {
					fmt.Fprintf(os.Stderr, "anneal-headless: improve: %v\n", ierr)
				} else {
					printImprove(ires)
				}
				continue
			}
			code = runTask(line)
		}
	}
	os.Exit(code)
}

func printImprove(res *improve.Result) {
	depth := ""
	if res.K > 1 {
		depth = fmt.Sprintf("  k=%d mode=%s", res.K, res.Mode)
	}
	fmt.Fprintf(os.Stderr, "improve %s → %s%s\n", res.RunID, res.Dir, depth)
	if res.Note != "" {
		fmt.Fprintf(os.Stderr, "  note: %s\n", res.Note)
	}
	for _, h := range res.Hints {
		fmt.Fprintf(os.Stderr, "  reviewer flag: %s\n", h)
	}
	for _, it := range res.Iterations {
		if it.Skipped != "" {
			fmt.Fprintf(os.Stderr, "  iter %d: %s\n", it.Iter, it.Skipped)
			continue
		}
		fmt.Fprintf(os.Stderr, "  iter %d: successor=%v entry=%s\n", it.Iter, it.Successor != "", it.Entry)
	}
	for _, c := range res.Candidates {
		if c.Skipped != "" {
			fmt.Fprintf(os.Stderr, "  candidate %s: skipped (%s)\n", c.Kind, c.Skipped)
			continue
		}
		steps := 0
		if c.Probe != nil {
			steps = len(c.Probe.Steps)
		}
		fmt.Fprintf(os.Stderr, "  candidate %s: probed (%d steps), contrast noted\n", c.Kind, steps)
	}
	if res.Decisions != "" {
		fmt.Fprintln(os.Stderr, "  decisions:")
		for _, l := range strings.Split(strings.TrimSpace(res.Decisions), "\n") {
			fmt.Fprintf(os.Stderr, "    %s\n", l)
		}
	}
	if res.Successor != "" {
		fmt.Fprintln(os.Stderr, "  successor applied as session overlay — the global harness is unchanged")
	} else {
		fmt.Fprintln(os.Stderr, "  no successor (composer kept the current harness)")
	}
}
