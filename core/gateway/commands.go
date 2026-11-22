package gateway

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

func (g *Gateway) handleInbound(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case in := <-g.in:
			g.onInbound(ctx, in)
		}
	}
}

func (g *Gateway) onInbound(ctx context.Context, in Inbound) {
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return
	}
	if text == "1" || text == "2" || text == "3" || text == "0" {
		if p, ok := g.state.pending(in.Chat); ok {
			g.answerApproval(ctx, in, p, text)
			return
		}
	}
	if strings.HasPrefix(text, "/") {
		g.command(ctx, in, text)
		return
	}
	g.sendAgent(ctx, in, text)
}

func (g *Gateway) serverFor(chat string) string {
	return g.state.activeServer(chat, g.cfg.DefaultServer)
}

func (g *Gateway) reply(in Inbound, msg Message) {
	g.sendChat(route{transport: in.Transport, chat: in.Chat}, msg)
}

func (g *Gateway) command(ctx context.Context, in Inbound, text string) {
	fields := strings.Fields(text)
	cmd := strings.ToLower(fields[0])
	args := fields[1:]
	server := g.serverFor(in.Chat)

	switch cmd {
	case "/help", "/?":
		g.reply(in, Message{Text: helpText, Markdown: true})
	case "/use":
		if len(args) == 0 {
			g.reply(in, Message{Text: "usage: /use " + strings.Join(g.serverNames(), "|")})
			return
		}
		if _, ok := g.cfg.Servers[args[0]]; !ok {
			g.reply(in, Message{Text: "unknown server " + args[0]})
			return
		}
		g.state.setActiveServer(in.Chat, args[0])
		g.reply(in, Message{Text: "using " + args[0]})
	case "/status":
		var info map[string]any
		if err := g.call(ctx, server, "server.info", nil, &info); err != nil {
			g.reply(in, Message{Text: "status: " + err.Error()})
			return
		}
		var q struct {
			Items  []any  `json:"items"`
			Paused bool   `json:"paused"`
			Reason string `json:"reason"`
		}
		_ = g.call(ctx, server, "queue.list", nil, &q)
		var jobs []map[string]any
		_ = g.call(ctx, server, "schedule.list", nil, &jobs)
		var sb strings.Builder
		fmt.Fprintf(&sb, "server: %s\nsession: %s\nmode: %s\nbusy: %v\nschedule busy: %v\nnext job: %s %s\nqueued: %d",
			server, str(info["session"]), str(info["mode"]), info["busy"], info["schedule_busy"],
			str(info["schedule_next"]), str(info["schedule_job"]), len(q.Items))
		if q.Paused {
			sb.WriteString("\nqueue paused: " + q.Reason)
		}
		if len(jobs) > 0 {
			sb.WriteString("\njobs:")
			for _, j := range jobs {
				sb.WriteString("\n- " + summarizeJob(j))
			}
		}
		g.reply(in, Message{Text: sb.String()})
	case "/stop", "/stop!":
		if err := g.call(ctx, server, "agent.interrupt", nil, nil); err != nil {
			g.reply(in, Message{Text: "stop: " + err.Error()})
			return
		}
		if cmd == "/stop!" {
			_ = g.call(ctx, server, "agent.interrupt", nil, nil)
			g.reply(in, Message{Text: "hard stop requested"})
			return
		}
		g.reply(in, Message{Text: "soft stop requested (send /stop again or /stop! to force)"})
	case "/new":
		if err := g.call(ctx, server, "sessions.new", nil, nil); err != nil {
			g.reply(in, Message{Text: "new session: " + err.Error()})
			return
		}
		g.reply(in, Message{Text: "new session started"})
	case "/sessions":
		var out []map[string]any
		if err := g.call(ctx, server, "sessions.list", nil, &out); err != nil {
			g.reply(in, Message{Text: "sessions: " + err.Error()})
			return
		}
		var sb strings.Builder
		sb.WriteString("sessions:")
		for i, s := range out {
			if i >= 15 {
				break
			}
			sb.WriteString("\n- " + str(s["id"]) + "  " + str(s["title"]))
		}
		if len(out) == 0 {
			sb.WriteString(" (none)")
		}
		g.reply(in, Message{Text: sb.String()})
	case "/open":
		if len(args) == 0 {
			g.reply(in, Message{Text: "usage: /open <session-id>"})
			return
		}
		if err := g.call(ctx, server, "sessions.open", map[string]any{"id": args[0]}, nil); err != nil {
			g.reply(in, Message{Text: "open: " + err.Error()})
			return
		}
		g.reply(in, Message{Text: "opened " + args[0]})
	case "/queue":
		var q struct {
			Items  []map[string]any `json:"items"`
			Paused bool             `json:"paused"`
			Reason string           `json:"reason"`
		}
		if err := g.call(ctx, server, "queue.list", nil, &q); err != nil {
			g.reply(in, Message{Text: "queue: " + err.Error()})
			return
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "queue: %d item(s), paused=%v", len(q.Items), q.Paused)
		if q.Reason != "" {
			sb.WriteString(" (" + q.Reason + ")")
		}
		for _, it := range q.Items {
			fmt.Fprintf(&sb, "\n- %s: %s", str(it["id"]), str(it["text"]))
		}
		g.reply(in, Message{Text: sb.String()})
	case "/cancel":
		if len(args) == 0 {
			g.reply(in, Message{Text: "usage: /cancel <input-id>"})
			return
		}
		var res map[string]any
		if err := g.call(ctx, server, "queue.cancel", map[string]any{"id": args[0]}, &res); err != nil {
			g.reply(in, Message{Text: "cancel: " + err.Error()})
			return
		}
		g.reply(in, Message{Text: "cancelled " + args[0]})
	case "/resume":
		if err := g.call(ctx, server, "queue.resume", nil, nil); err != nil {
			g.reply(in, Message{Text: "resume: " + err.Error()})
			return
		}
		g.reply(in, Message{Text: "queue resumed"})
	case "/approve":
		if len(args) < 2 {
			g.reply(in, Message{Text: "usage: /approve <id> once|session|always|reject"})
			return
		}
		action := normalizeApproval(args[1])
		if action == "" {
			g.reply(in, Message{Text: "action must be once|session|always|reject"})
			return
		}
		if err := g.call(ctx, server, "perm.respond", map[string]any{"id": args[0], "action": action}, nil); err != nil {
			g.reply(in, Message{Text: "approve: " + err.Error()})
			return
		}
		g.state.clearPending(in.Chat, args[0])
		g.reply(in, Message{Text: "approval sent: " + args[1]})
	case "/schedule", "/cron":
		g.scheduleCommand(ctx, in, server, args)
	default:
		g.reply(in, Message{Text: "unknown command " + cmd + " — try /help"})
	}
}

func (g *Gateway) scheduleCommand(ctx context.Context, in Inbound, server string, args []string) {
	sub := "list"
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}
	rest := args
	if len(args) > 0 {
		rest = args[1:]
	}
	needID := func() string {
		if len(rest) == 0 {
			g.reply(in, Message{Text: "usage: /schedule " + sub + " <id>"})
			return ""
		}
		return rest[0]
	}
	switch sub {
	case "list", "ls":
		var jobs []map[string]any
		if err := g.call(ctx, server, "schedule.list", nil, &jobs); err != nil {
			g.reply(in, Message{Text: "schedule: " + err.Error()})
			return
		}
		if len(jobs) == 0 {
			g.reply(in, Message{Text: "no scheduled jobs"})
			return
		}
		var sb strings.Builder
		sb.WriteString("scheduled jobs:")
		for _, j := range jobs {
			sb.WriteString("\n- " + summarizeJob(j))
		}
		g.reply(in, Message{Text: sb.String()})
	case "get":
		id := needID()
		if id == "" {
			return
		}
		var job map[string]any
		if err := g.call(ctx, server, "schedule.get", map[string]any{"id": id}, &job); err != nil {
			g.reply(in, Message{Text: "schedule: " + err.Error()})
			return
		}
		g.reply(in, Message{Text: summarizeJob(job) + "\nprompt: " + trimText(str(job["prompt"]), 1500)})
	case "run":
		id := needID()
		if id == "" {
			return
		}
		var res map[string]any
		if err := g.call(ctx, server, "schedule.run", map[string]any{"id": id}, &res); err != nil {
			g.reply(in, Message{Text: "schedule run: " + err.Error()})
			return
		}
		g.reply(in, Message{Text: "queued manual run " + str(res["id"]) + " for " + id})
	case "pause", "resume", "rm", "remove":
		id := needID()
		if id == "" {
			return
		}
		method := "schedule." + sub
		if sub == "rm" {
			method = "schedule.remove"
		}
		if sub == "remove" {
			method = "schedule.remove"
		}
		if err := g.call(ctx, server, method, map[string]any{"id": id}, nil); err != nil {
			g.reply(in, Message{Text: "schedule: " + err.Error()})
			return
		}
		g.reply(in, Message{Text: sub + " " + id})
	case "runs":
		var runs []map[string]any
		if err := g.call(ctx, server, "schedule.runs", map[string]any{"limit": 10}, &runs); err != nil {
			g.reply(in, Message{Text: "schedule runs: " + err.Error()})
			return
		}
		var sb strings.Builder
		sb.WriteString("recent runs:")
		for _, r := range runs {
			fmt.Fprintf(&sb, "\n- %s %s %s: %s", str(r["job_id"]), str(r["status"]), str(r["started_at"]), trimText(str(r["text"]), 160))
		}
		if len(runs) == 0 {
			sb.WriteString(" (none)")
		}
		g.reply(in, Message{Text: sb.String()})
	default:
		g.reply(in, Message{Text: "usage: /schedule list|get|run|pause|resume|rm|runs [id]"})
	}
}

func (g *Gateway) sendAgent(ctx context.Context, in Inbound, text string) {
	server := g.serverFor(in.Chat)
	ref := makeRef(route{transport: in.Transport, chat: in.Chat})
	var res struct {
		OK       bool `json:"ok"`
		ID       any  `json:"id"`
		Run      int  `json:"run"`
		Queued   bool `json:"queued"`
		Position int  `json:"position"`
	}
	if err := g.call(ctx, server, "agent.send", map[string]any{
		"text": text, "queue": true, "ref": ref,
	}, &res); err != nil {
		g.reply(in, Message{Text: "send: " + err.Error()})
		return
	}
	if res.Queued {
		g.reply(in, Message{Text: fmt.Sprintf("queued (#%d)", res.Position)})
	}
}

func (g *Gateway) answerApproval(ctx context.Context, in Inbound, p pendingApproval, choice string) {
	action := map[string]string{"1": "once", "2": "allow_session", "3": "always", "0": "reject"}[choice]
	if action == "" {
		return
	}
	if err := g.call(ctx, p.Server, "perm.respond", map[string]any{"id": p.ID, "action": action}, nil); err != nil {
		g.reply(in, Message{Text: "approval: " + err.Error()})
		return
	}
	g.state.clearPending(in.Chat, p.ID)
	g.reply(in, Message{Text: "approval sent: " + action})
}

func normalizeApproval(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "once", "allow":
		return "once"
	case "2", "session", "allow_session":
		return "allow_session"
	case "3", "always":
		return "always"
	case "0", "reject", "deny":
		return "reject"
	}
	return ""
}

func (g *Gateway) serverNames() []string {
	out := make([]string, 0, len(g.cfg.Servers))
	for name := range g.cfg.Servers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

const helpText = `anneal gateway commands:
/status — server, session, queue and next job
/stop — soft stop (again or /stop! to force)
/new — new session
/sessions — recent sessions
/open <id> — open a session
/queue — queued inputs
/cancel <id> — remove a queued input
/resume — resume a paused queue
/approve <id> once|session|always|reject — answer an approval
/schedule list|get|run|pause|resume|rm|runs [id] — manage jobs
/use <server> — switch anneald instance
/help — this text
Anything else is sent to anneal as a message.`
