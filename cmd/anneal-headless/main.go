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
	"github.com/uidbz/anneal/core/client"
	"github.com/uidbz/anneal/core/improve"
)

// backend abstracts "drive the core": the local App (embedded core) or the
// protocol client (anneald over -server). Commands go through these methods;
// events arrive on the shared sink channel either way.
type backend interface {
	startTask(text string) (int, bool)
	interrupt()
	newSession()
	openSession(id string)
	respondPerm(id, action string)
	setMode(mode string)
	learnNow()
	busy() bool
	dead() <-chan struct{} // nil channel = never fires (local)
	isRemote() bool
	call(method string, params any) error // remote RPC passthrough (no-op locally)
}

type localBackend struct{ a *app.App }

func (l *localBackend) startTask(t string) (int, bool) { return l.a.Runner.StartTask(t) }
func (l *localBackend) interrupt()                     { l.a.Runner.Interrupt() }
func (l *localBackend) newSession()                    { l.a.Runner.NewSession() }
func (l *localBackend) openSession(id string)          { l.a.Runner.OpenSession(id) }
func (l *localBackend) respondPerm(id, a string)       { l.a.Runner.RespondPerm(id, a) }
func (l *localBackend) setMode(m string)               { l.a.Runner.SetMode(m) }
func (l *localBackend) learnNow()                      { l.a.Review.LearnNow() }
func (l *localBackend) busy() bool                     { return l.a.Runner.Busy() }
func (l *localBackend) dead() <-chan struct{}          { return nil }
func (l *localBackend) isRemote() bool                 { return false }
func (l *localBackend) call(string, any) error         { return fmt.Errorf("remote call from local mode") }

type remoteBackend struct{ c *client.Client }

func (r *remoteBackend) startTask(t string) (int, bool) {
	var res struct {
		ID int  `json:"id"`
		OK bool `json:"ok"`
	}
	if err := r.callJSON("agent.send", map[string]any{"text": t}, &res); err != nil {
		fmt.Fprintln(os.Stderr, "anneal-headless: send:", err)
		return 0, false
	}
	return res.ID, res.OK
}
func (r *remoteBackend) interrupt() {
	if err := r.call("agent.interrupt", nil); err != nil {
		fmt.Fprintln(os.Stderr, "anneal-headless: interrupt:", err)
	}
}
func (r *remoteBackend) newSession()           { r.call("sessions.new", nil) }
func (r *remoteBackend) openSession(id string) { r.call("sessions.open", map[string]any{"id": id}) }
func (r *remoteBackend) respondPerm(id, a string) {
	r.call("perm.respond", map[string]any{"id": id, "action": a})
}
func (r *remoteBackend) setMode(m string)      { r.call("mode.set", map[string]any{"mode": m}) }
func (r *remoteBackend) learnNow()             { r.call("learn.now", nil) }
func (r *remoteBackend) isRemote() bool        { return true }
func (r *remoteBackend) dead() <-chan struct{} { return r.c.Dead() }
func (r *remoteBackend) busy() bool {
	var res struct {
		Busy bool `json:"busy"`
	}
	if err := r.callJSON("run.status", nil, &res); err != nil {
		return false
	}
	return res.Busy
}
func (r *remoteBackend) call(method string, params any) error {
	_, err := r.callRaw(method, params)
	return err
}
func (r *remoteBackend) callRaw(method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return r.c.Call(ctx, method, params)
}
func (r *remoteBackend) callJSON(method string, params, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return r.c.CallInto(ctx, method, params, out)
}

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
	improve  chan bool // true = improve.done ok; false = improve.error
	be       backend
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
	case "improve.error":
		if !s.jsonMode {
			fmt.Printf("🔧 improve failed: %v\n", e["error"])
		}
		select {
		case s.improve <- false:
		default:
		}
	case "improve.done":
		ok, _ := e["ok"].(bool)
		select {
		case s.improve <- ok:
		default:
		}
		if s.jsonMode {
			return
		}
		if ok {
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
	go s.be.respondPerm(id, answer)
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
	serverAddr := flag.String("server", "", "drive a remote anneald (ws://host:port, host:port, or unix:/path) instead of the local core")
	flag.Parse()

	if *prompt == "" && !*interactive {
		fmt.Fprintln(os.Stderr, `anneal-headless: give -p "task" or -i (REPL); -h for flags`)
		os.Exit(2)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var a *app.App // local mode only (improve/results printing)
	var be backend
	events := make(chan any, 1024)

	if *serverAddr != "" {
		c, err := client.Dial(ctx, *serverAddr, os.Getenv("ANNEAL_AUTH_TOKEN"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "anneal-headless: server:", err)
			os.Exit(1)
		}
		defer c.Close()
		be = &remoteBackend{c: c}
		go func() {
			for e := range c.Events() {
				events <- e
			}
		}()
		fmt.Fprintf(os.Stderr, "anneal-headless: connected to %s (server %s)\n", *serverAddr, c.ServerVersion())
	} else {
		var aerr error
		a, aerr = app.New()
		if aerr != nil {
			fmt.Fprintln(os.Stderr, "anneal-headless:", aerr)
			os.Exit(1)
		}
		be = &localBackend{a: a}
		// One ordered consumer: a goroutine per event would reorder text deltas
		// and could deliver run.finished before message.final/tool.result.
		a.Bus.AddTap(func(v any) { events <- v })
		a.StartBlocking(ctx) // monitor + review async; tie/MCP connected inline
	}

	s := &sink{jsonMode: *jsonMode, verbose: *verbose,
		done: make(chan runResult, 1), learn: make(chan struct{}, 1),
		improve: make(chan bool, 1), be: be,
		denyAll: !*yolo && *allowList == "", allow: map[string]bool{}}
	for _, t := range strings.Split(*allowList, ",") {
		if t != "" {
			s.allow[t] = true
		}
	}
	if *yolo {
		s.allow["*"] = true
		be.setMode("bypassPermissions")
	}
	go func() {
		for v := range events {
			s.handle(v)
		}
	}()

	if *newSess {
		be.newSession()
	} else if *session != "" {
		be.openSession(*session)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		if !be.busy() {
			cancel()
			os.Exit(130) // idle: quit immediately (pre-split semantics)
		}
		be.interrupt()
		fmt.Fprintln(os.Stderr, "(interrupt sent; Ctrl-C again to quit)")
		<-sig // second signal: hard exit
		cancel()
		os.Exit(130)
	}()

	runTask := func(task string) int {
		id, ok := be.startTask(task)
		if !ok {
			fmt.Fprintln(os.Stderr, "anneal-headless: a run is already active")
			return 1
		}
		s.setAwait(id)
		select {
		case res := <-s.done:
			s.setAwait(0)
			if *learn {
				be.learnNow()
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
				code = finishImprove(be, *improveK, *improveMode, *improveResume, s.improve)
			}
			return code
		case <-ctx.Done():
			s.discardRun(id)
			return 130
		case <-be.dead(): // nil channel locally: never fires
			s.discardRun(id)
			fmt.Fprintln(os.Stderr, "anneal-headless: server connection lost")
			return 1
		case <-time.After(60 * time.Minute):
			s.discardRun(id)
			be.interrupt()
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
				be.newSession()
				fmt.Fprintln(os.Stderr, "(new session)")
				continue
			case "/interrupt":
				be.interrupt()
				continue
			}
			if strings.HasPrefix(line, "/harness") {
				fields := strings.Fields(line)
				if be.isRemote() {
					rb := be.(*remoteBackend)
					switch {
					case len(fields) >= 3 && fields[1] == "promote":
						raw, err := rb.callRaw("harness.lib_promote", map[string]any{"id": fields[2]})
						if err != nil {
							fmt.Fprintln(os.Stderr, "promote:", err)
						} else {
							fmt.Fprintln(os.Stderr, string(raw))
						}
					case len(fields) >= 2 && fields[1] == "list":
						raw, err := rb.callRaw("harness.lib", nil)
						if err != nil {
							fmt.Fprintln(os.Stderr, "library:", err)
						} else {
							fmt.Fprintln(os.Stderr, string(raw))
						}
					default:
						raw, err := rb.callRaw("harness.info", nil)
						if err != nil {
							fmt.Fprintln(os.Stderr, "harness:", err)
						} else {
							fmt.Fprintln(os.Stderr, string(raw))
						}
						if raw, err := rb.callRaw("flags.peek", map[string]any{"n": 5}); err == nil {
							fmt.Fprintln(os.Stderr, "reviewer flags:", string(raw))
						}
					}
					continue
				}
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
				if rb, ok := be.(*remoteBackend); ok {
					fmt.Fprintln(os.Stderr, "🔧 harness improvement started on the server (progress streams below)")
					if err := rb.call("improve.start", map[string]any{
						"k": *improveK, "mode": *improveMode, "resume": *improveResume}); err != nil {
						fmt.Fprintln(os.Stderr, "anneal-headless: improve:", err)
					}
					continue
				}
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

// finishImprove runs the -improve tail: locally (returns the structured
// result) or remotely (start RPC, then wait for the improve.done/error
// event that the sink forwards on done).
func finishImprove(be backend, k int, mode, resume string, done chan bool) int {
	fmt.Fprintln(os.Stderr, "🔧 harness improvement: propose → probe → compose …")
	if lb, ok := be.(*localBackend); ok {
		ictx, icancel := context.WithTimeout(context.Background(), 25*time.Minute)
		defer icancel()
		ires, ierr := lb.a.ImproveWith(ictx, k, mode, resume)
		if ierr != nil {
			fmt.Fprintf(os.Stderr, "anneal-headless: improve: %v\n", ierr)
			return 1
		}
		printImprove(ires)
		return 0
	}
	rb := be.(*remoteBackend)
	if err := rb.call("improve.start", map[string]any{"k": k, "mode": mode, "resume": resume}); err != nil {
		fmt.Fprintln(os.Stderr, "anneal-headless: improve:", err)
		return 1
	}
	select {
	case ok := <-done:
		if !ok {
			return 1
		}
		return 0
	case <-be.dead():
		fmt.Fprintln(os.Stderr, "anneal-headless: server connection lost during improve")
		return 1
	case <-time.After(25 * time.Minute):
		fmt.Fprintln(os.Stderr, "anneal-headless: improve timed out")
		return 1
	}
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
