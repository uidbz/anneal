package gateway

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/uidbz/anneal/core/client"
	"github.com/uidbz/anneal/core/schedule"
)

// Inbound is one authorized message from a chat.
type Inbound struct {
	Transport string
	Chat      string
	ID        string
	Text      string
	Time      time.Time
}

// Message is one outbound notification or reply.
type Message struct {
	Title    string
	Text     string
	Priority int
	Tags     []string
	Markdown bool
}

// Cursor persists a transport's delivery position (ntfy last message id).
type Cursor interface {
	Cursor(key string) string
	SetCursor(key, value string)
}

// Transport is one messaging backend.
type Transport interface {
	Name() string
	HomeChat() string
	Start(ctx context.Context, inbox chan<- Inbound, cursor Cursor) error
	Send(ctx context.Context, chat string, msg Message) error
	Close() error
}

type route struct {
	transport string
	chat      string
}

type delivery struct {
	transport string
	chat      string
	msg       Message
}

// Gateway fans anneald events into messaging transports and converts inbound
// chat text into protocol calls.
type Gateway struct {
	cfg   Config
	state *stateStore

	mu       sync.RWMutex
	clients  map[string]*client.Client
	runRefs  map[string]map[int]route // server -> run id -> origin chat
	lastSeen map[string]string        // server -> provider health state

	transports map[string]Transport
	out        chan delivery
	in         chan Inbound

	closeOnce sync.Once
}

// New builds a gateway. Run starts it.
func New(cfg Config) (*Gateway, error) {
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	st, err := openState(cfg.StatePath)
	if err != nil {
		return nil, err
	}
	g := &Gateway{
		cfg:        cfg,
		state:      st,
		clients:    map[string]*client.Client{},
		runRefs:    map[string]map[int]route{},
		lastSeen:   map[string]string{},
		transports: map[string]Transport{},
		out:        make(chan delivery, 256),
		in:         make(chan Inbound, 128),
	}
	for _, tc := range cfg.Transports {
		switch tc.Type {
		case "ntfy":
			t, err := newNtfyTransport(tc)
			if err != nil {
				return nil, err
			}
			g.transports[t.Name()] = t
		case "matrix":
			var t Transport
			var err error
			if tc.Encrypted {
				t, err = newMatrixE2EETransport(tc)
			} else {
				t, err = newMatrixTransport(tc)
			}
			if err != nil {
				return nil, err
			}
			g.transports[t.Name()] = t
		default:
			return nil, fmt.Errorf("gateway: unsupported transport %q", tc.Type)
		}
	}
	return g, nil
}

// Run blocks until ctx is cancelled.
func (g *Gateway) Run(ctx context.Context) error {
	log.Printf("gateway: %d server(s), %d transport(s)", len(g.cfg.Servers), len(g.transports))
	go g.dispatch(ctx)
	go g.handleInbound(ctx)
	var wg sync.WaitGroup
	for _, t := range g.transports {
		t := t
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := t.Start(ctx, g.in, g.state); err != nil && ctx.Err() == nil {
				log.Printf("gateway: transport %s stopped: %v", t.Name(), err)
			}
		}()
	}
	for name, sc := range g.cfg.Servers {
		name, sc := name, sc
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.runServer(ctx, name, sc)
		}()
	}
	<-ctx.Done()
	g.closeOnce.Do(func() {
		g.mu.Lock()
		for _, c := range g.clients {
			_ = c.Close()
		}
		g.clients = map[string]*client.Client{}
		g.mu.Unlock()
		for _, t := range g.transports {
			_ = t.Close()
		}
	})
	wg.Wait()
	return nil
}

func (g *Gateway) dispatch(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case d := <-g.out:
			t := g.transports[d.transport]
			if t == nil {
				continue
			}
			sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := t.Send(sendCtx, d.chat, d.msg); err != nil {
				log.Printf("gateway: send via %s: %v", d.transport, err)
			}
			cancel()
		}
	}
}

// runServer maintains one anneald connection with exponential backoff.
func (g *Gateway) runServer(ctx context.Context, name string, sc ServerConfig) {
	token := ""
	if sc.TokenEnv != "" {
		token = os.Getenv(sc.TokenEnv)
	}
	backoff := time.Second
	for ctx.Err() == nil {
		c, err := client.Dial(ctx, sc.Addr, token)
		if err != nil {
			log.Printf("gateway: %s: dial %s: %v", name, sc.Addr, err)
			if !sleepCtx(ctx, backoff) {
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		g.mu.Lock()
		g.clients[name] = c
		g.mu.Unlock()
		log.Printf("gateway: %s: connected to %s (%s)", name, sc.Addr, c.ServerVersion())
		go g.catchUp(ctx, name)

		for {
			select {
			case <-ctx.Done():
				_ = c.Close()
				return
			case <-c.Dead():
				goto reconnect
			case ev, ok := <-c.Events():
				if !ok {
					goto reconnect
				}
				g.onEvent(name, ev)
			}
		}
	reconnect:
		g.mu.Lock()
		if g.clients[name] == c {
			delete(g.clients, name)
		}
		g.mu.Unlock()
		_ = c.Close()
		if !sleepCtx(ctx, backoff) {
			return
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (g *Gateway) client(server string) *client.Client {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.clients[server]
}

func (g *Gateway) call(ctx context.Context, server, method string, params, out any) error {
	c := g.client(server)
	if c == nil {
		return fmt.Errorf("server %q is not connected", server)
	}
	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return c.CallInto(callCtx, method, params, out)
}

func (g *Gateway) onEvent(server string, ev client.Event) {
	typ := ev.Type()
	switch typ {
	case "run.started":
		ref, _ := ev["ref"].(string)
		run, ok := asInt(ev["run"])
		if !ok {
			return
		}
		if r, ok := parseRef(ref); ok {
			g.mu.Lock()
			if g.runRefs[server] == nil {
				g.runRefs[server] = map[int]route{}
			}
			g.runRefs[server][run] = r
			g.mu.Unlock()
		}
	case "run.finished":
		run, _ := asInt(ev["run"])
		g.mu.Lock()
		r, ok := g.runRefs[server][run]
		delete(g.runRefs[server], run)
		g.mu.Unlock()
		if ok {
			g.sendChat(r, formatRun(server, ev))
		} else if g.cfg.NotifyForeground {
			g.notifyDefault(formatRun(server, ev))
		}
	case "schedule.finished":
		g.onScheduleFinished(server, ev)
	case "schedule.error":
		g.onScheduleProblem(server, ev, "scheduled task failed")
	case "schedule.missed":
		g.onScheduleProblem(server, ev, "scheduled task missed its slot")
	case "perm.request":
		g.onPermRequest(server, ev)
	case "perm.resolved":
		id, _ := ev["id"].(string)
		for _, t := range g.transports {
			g.state.clearPending(t.HomeChat(), id)
		}
	case "provider.state":
		g.onProvider(server, ev)
	case "server.shutting_down":
		reason, _ := ev["reason"].(string)
		g.notify(Message{
			Title: "anneal is shutting down",
			Text:  fmt.Sprintf("%s: %s", server, reason),
			Tags:  []string{"warning"},
		}, nil)
	}
}

func (g *Gateway) onScheduleFinished(server string, ev client.Event) {
	job := eventMap(ev["job"])
	rec := eventMap(ev["record"])
	name := firstString(job["name"], job["id"], "scheduled task")
	status := str(rec["status"])
	if status == "" {
		if b, _ := rec["ok"].(bool); b {
			status = "ok"
		} else {
			status = "failed"
		}
	}
	text := str(rec["text"])
	if silent(text) {
		return
	}
	secs, _ := asInt(rec["duration_s"])
	header := fmt.Sprintf("%s - %s", name, statusWord(status))
	if secs > 0 {
		header += fmt.Sprintf(" in %ds", secs)
	}
	body := text
	if body == "" {
		body = str(rec["error"])
	}
	if body == "" {
		body = "(no output)"
	}
	next := ""
	if jn := str(job["next_run"]); jn != "" {
		next = "\nnext: " + jn
	} else if rn := str(rec["next_run"]); rn != "" {
		next = "\nnext: " + rn
	}
	if sid := str(rec["session_id"]); sid != "" {
		next += "\nsession: " + sid
	}
	msg := Message{
		Title:    "anneal: " + header,
		Text:     trimText(body, 3500) + next,
		Priority: priorityFor(status),
		Tags:     tagsFor(status),
		Markdown: true,
	}
	g.notify(msg, deliverTargets(job))
	if id := str(rec["id"]); id != "" {
		g.state.setLastRun(server, id)
	}
}

func (g *Gateway) onScheduleProblem(server string, ev client.Event, title string) {
	job := eventMap(ev["job"])
	name := firstString(job["name"], job["id"], "scheduled task")
	text := str(ev["error"])
	if text == "" {
		text = str(ev["next_run"])
	}
	g.notify(Message{
		Title:    "anneal: " + title + ": " + name,
		Text:     text,
		Priority: 4,
		Tags:     []string{"warning"},
	}, deliverTargets(job))
}

// catchUp delivers schedule runs completed while the gateway was offline. It
// uses the durable run ledger and anchors on the last delivered record id; if
// there is no anchor yet it records the newest result without replaying years
// of history.
func (g *Gateway) catchUp(ctx context.Context, server string) {
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var runs []schedule.RunRecord
	if err := g.call(callCtx, server, "schedule.runs", map[string]any{"limit": 100}, &runs); err != nil || len(runs) == 0 {
		return
	}
	last := g.state.lastRun(server)
	if last == "" {
		g.state.setLastRun(server, runs[0].ID)
		return
	}
	idx := -1
	for i := range runs {
		if runs[i].ID == last {
			idx = i
			break
		}
	}
	if idx < 0 {
		// The ledger rotated past our anchor; do not risk a flood of stale
		// notifications. Start from the newest result.
		g.state.setLastRun(server, runs[0].ID)
		return
	}
	// runs are newest-first. Deliver the newer entries oldest-first.
	for i := idx - 1; i >= 0; i-- {
		g.deliverRunRecord(server, runs[i])
	}
	g.state.setLastRun(server, runs[0].ID)
}

func (g *Gateway) deliverRunRecord(server string, rec schedule.RunRecord) {
	if rec.Status == schedule.StatusRunning || silent(rec.Text) {
		return
	}
	header := fmt.Sprintf("%s - %s", firstString(rec.JobName, rec.JobID, "scheduled task"), statusWord(rec.Status))
	if rec.DurationS > 0 {
		header += fmt.Sprintf(" in %ds", rec.DurationS)
	}
	body := rec.Text
	if body == "" {
		body = rec.Error
	}
	if body == "" {
		body = "(no output)"
	}
	suffix := ""
	if rec.NextRun != nil {
		suffix += "\nnext: " + rec.NextRun.UTC().Format(time.RFC3339)
	}
	if rec.SessionID != "" {
		suffix += "\nsession: " + rec.SessionID
	}
	g.notify(Message{
		Title:    "anneal: " + header,
		Text:     trimText(body, 3500) + suffix,
		Priority: priorityFor(rec.Status),
		Tags:     tagsFor(rec.Status),
		Markdown: true,
	}, rec.Deliver)
}

func (g *Gateway) onPermRequest(server string, ev client.Event) {
	id := str(ev["id"])
	if len(g.transports) == 0 {
		return
	}
	job := firstString(ev["job_name"], ev["job_id"])
	head := "Approval needed"
	if job != "" {
		head += " (" + job + ")"
	}
	text := fmt.Sprintf("%s\n%s\n\naction: %s\n%s", head, str(ev["message"]), str(ev["action"]), resourcesText(ev["resources"]))
	if preview := str(ev["input"]); preview != "" && len(preview) < 1200 {
		text += "\n\n" + preview
	}
	text += "\n\nreply: 1 once · 2 this session · 3 always · 0 reject\nid: " + id
	for _, t := range g.transports {
		g.state.setPending(t.HomeChat(), pendingApproval{Server: server, ID: id, At: time.Now()})
	}
	g.notify(Message{Title: "anneal approval", Text: text, Priority: 5, Tags: []string{"lock"}}, nil)
}

func (g *Gateway) onProvider(server string, ev client.Event) {
	health := eventMap(ev["health"])
	state := str(health["state"])
	if state == "" {
		return
	}
	g.mu.Lock()
	prev := g.lastSeen[server]
	g.lastSeen[server] = state
	g.mu.Unlock()
	if state == prev {
		return
	}
	switch state {
	case "unreachable", "wrong_service":
		g.notify(Message{
			Title: "anneal model server " + state,
			Text:  fmt.Sprintf("%s: %s", server, str(health["error"])),
			Tags:  []string{"warning"},
		}, nil)
	case "online":
		if prev != "" {
			g.notify(Message{Title: "anneal model server online", Text: server, Tags: []string{"white_check_mark"}}, nil)
		}
	}
}

func (g *Gateway) notify(msg Message, deliver []string) {
	allow := map[string]bool{}
	all := len(deliver) == 0
	for _, d := range deliver {
		if d == "all" {
			all = true
		}
		allow[d] = true
	}
	for name, t := range g.transports {
		if !all && !allow[name] {
			continue
		}
		g.sendChat(route{transport: name, chat: t.HomeChat()}, msg)
	}
}

func (g *Gateway) notifyDefault(msg Message) {
	if r := g.defaultRoute(); r.transport != "" {
		g.sendChat(r, msg)
	}
}

func (g *Gateway) sendChat(r route, msg Message) {
	if r.transport == "" {
		return
	}
	if msg.Title == "" {
		msg.Title = "anneal"
	}
	select {
	case g.out <- delivery{transport: r.transport, chat: r.chat, msg: msg}:
	default:
		log.Printf("gateway: outbound queue full, dropping %q", msg.Title)
	}
}

func (g *Gateway) defaultRoute() route {
	names := make([]string, 0, len(g.transports))
	for name := range g.transports {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return route{}
	}
	name := names[0]
	return route{transport: name, chat: g.transports[name].HomeChat()}
}

func parseRef(ref string) (route, bool) {
	parts := strings.SplitN(ref, "|", 3)
	if len(parts) != 3 || parts[0] != "gateway" {
		return route{}, false
	}
	return route{transport: parts[1], chat: parts[2]}, true
}

func makeRef(r route) string { return "gateway|" + r.transport + "|" + r.chat }

func eventMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func deliverTargets(job map[string]any) []string {
	raw, ok := job["deliver"].([]any)
	if !ok {
		return []string{"all"}
	}
	var out []string
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		out = []string{"all"}
	}
	return out
}

func resourcesText(v any) string {
	raw, ok := v.([]any)
	if !ok {
		return ""
	}
	var out []string
	for _, x := range raw {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return "resources: " + strings.Join(out, ", ")
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func firstString(vals ...any) string {
	for _, v := range vals {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}
