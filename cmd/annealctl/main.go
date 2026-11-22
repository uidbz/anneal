// annealctl is a small protocol client for status, messages, and schedules.
//
//	annealctl -server 127.0.0.1:7777 status
//	annealctl schedule add -id daily -schedule "0 7 * * 1-5" -prompt "..."
//	annealctl schedule list
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/uidbz/anneal/core/client"
	"github.com/uidbz/anneal/core/permission"
	"github.com/uidbz/anneal/core/schedule"
)

var version = "dev"

func main() {
	server := flag.String("server", envDefault("ANNEAL_SERVER", "127.0.0.1:7777"), "anneald address (host:port, unix:/path, ws://...)")
	tokenEnv := flag.String("token-env", "ANNEAL_AUTH_TOKEN", "env var holding the hello token")
	flag.Usage = usage
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, *server, os.Getenv(*tokenEnv))
	if err != nil {
		fatal(err)
	}
	defer c.Close()

	switch args[0] {
	case "status":
		var out map[string]any
		if err := call(ctx, c, "server.info", nil, &out); err != nil {
			fatal(err)
		}
		printJSON(out)
	case "send":
		if len(args) < 2 {
			fatal(fmt.Errorf("usage: annealctl send <text>"))
		}
		var out map[string]any
		if err := call(ctx, c, "agent.send", map[string]any{
			"text": strings.Join(args[1:], " "), "queue": true,
		}, &out); err != nil {
			fatal(err)
		}
		printJSON(out)
	case "schedule":
		scheduleCmd(ctx, c, args[1:])
	case "sessions":
		var out []map[string]any
		if err := call(ctx, c, "sessions.list", nil, &out); err != nil {
			fatal(err)
		}
		for _, s := range out {
			fmt.Printf("%-28s %-12s %s\n", str(s["id"]), str(s["origin"]), str(s["title"]))
		}
	case "version", "-version", "--version":
		fmt.Println("annealctl", version)
	default:
		fatal(fmt.Errorf("unknown command %q (try status, send, schedule, sessions)", args[0]))
	}
}

func scheduleCmd(ctx context.Context, c *client.Client, args []string) {
	if len(args) == 0 {
		fatal(fmt.Errorf("usage: annealctl schedule list|get|add|run|pause|resume|rm|runs"))
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list", "ls":
		var jobs []schedule.Job
		if err := call(ctx, c, "schedule.list", nil, &jobs); err != nil {
			fatal(err)
		}
		for _, j := range jobs {
			next := "—"
			if j.NextRun != nil {
				next = j.NextRun.Local().Format(time.RFC3339)
			}
			state := "enabled"
			if !j.IsEnabled() {
				state = "paused"
			}
			fmt.Printf("%-18s %-8s next=%-25s last=%-8s %s\n", j.ID, state, next, j.LastStatus, j.Name)
		}
	case "get":
		if len(rest) < 1 {
			fatal(fmt.Errorf("usage: schedule get <id>"))
		}
		var out map[string]any
		if err := call(ctx, c, "schedule.get", map[string]any{"id": rest[0]}, &out); err != nil {
			fatal(err)
		}
		printJSON(out)
	case "add":
		fs := flag.NewFlagSet("schedule add", flag.ExitOnError)
		id := fs.String("id", "", "job id (default: slug of name/prompt)")
		name := fs.String("name", "", "human name")
		prompt := fs.String("prompt", "", "self-contained task prompt (required)")
		expr := fs.String("schedule", "", "cron, \"every 2h\", \"30m\", or ISO timestamp (required)")
		tz := fs.String("timezone", "", "IANA timezone (default: schedule.timezone)")
		work := fs.String("work", "", "work-root-relative directory")
		session := fs.String("session", "fresh", "fresh|sticky")
		mode := fs.String("permission-mode", "dontAsk", "default|plan|acceptEdits|bypassPermissions|dontAsk")
		deliver := fs.String("deliver", "all", "comma-separated transports")
		overlap := fs.String("overlap", "skip", "skip|queue")
		missed := fs.String("missed", "skip", "skip|run_once")
		timeout := fs.Int("timeout", 0, "run timeout seconds (0 = config default)")
		enabled := fs.Bool("enabled", true, "start enabled")
		var allows multiFlag
		fs.Var(&allows, "allow", "per-job allow rule: action or action:resource (repeatable), e.g. gentoo or bash:git status *")
		_ = fs.Parse(rest)
		if *prompt == "" || *expr == "" {
			fatal(fmt.Errorf("-prompt and -schedule are required"))
		}
		ev := *enabled
		job := schedule.Job{
			ID: *id, Name: *name, Prompt: *prompt, Schedule: *expr, Timezone: *tz,
			Work: *work, Session: *session, PermissionMode: *mode,
			Deliver: splitCSV(*deliver), Overlap: *overlap, Missed: *missed,
			TimeoutS: *timeout, Enabled: &ev, Origin: "cli",
			Permission: permissionRules(allows),
		}
		var out schedule.Job
		if err := call(ctx, c, "schedule.create", job, &out); err != nil {
			fatal(err)
		}
		printJSON(out)
	case "rm", "remove":
		idArg(sub, rest)
		var out map[string]any
		if err := call(ctx, c, "schedule.remove", map[string]any{"id": rest[0]}, &out); err != nil {
			fatal(err)
		}
		fmt.Println("removed", rest[0])
	case "pause", "resume", "run":
		idArg(sub, rest)
		var out map[string]any
		if err := call(ctx, c, "schedule."+sub, map[string]any{"id": rest[0]}, &out); err != nil {
			fatal(err)
		}
		printJSON(out)
	case "runs":
		var out []schedule.RunRecord
		if err := call(ctx, c, "schedule.runs", map[string]any{"limit": 20}, &out); err != nil {
			fatal(err)
		}
		for _, r := range out {
			fmt.Printf("%-10s %-10s %-8s %s\n", r.JobID, r.Status, r.StartedAt.Local().Format("01-02 15:04"), clip(r.Text, 100))
		}
	default:
		fatal(fmt.Errorf("unknown schedule command %q", sub))
	}
}

func call(ctx context.Context, c *client.Client, method string, params, out any) error {
	return c.CallInto(ctx, method, params, out)
}

func printJSON(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func idArg(sub string, rest []string) {
	if len(rest) < 1 {
		fatal(fmt.Errorf("usage: schedule %s <id>", sub))
	}
}

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func permissionRules(allows []string) permission.Config {
	var out permission.Config
	for _, a := range allows {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		action, resource, _ := strings.Cut(a, ":")
		action = strings.TrimSpace(action)
		resource = strings.TrimSpace(resource)
		if resource == "" {
			resource = "*"
		}
		out = append(out, permission.Rule{Action: action, Resource: resource, Effect: permission.Allow})
	}
	return out
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "annealctl:", err)
	os.Exit(1)
}

func usage() {
	fmt.Fprintf(os.Stderr, `annealctl %s — anneald protocol client

Usage:
  annealctl [-server addr] [-token-env VAR] <command> [args]

Commands:
  status                 server/session/schedule status
  send <text>            queue a message for the agent
  sessions               list sessions
  schedule list          list jobs
  schedule get <id>      show one job
  schedule add [flags]   create a job (see -h)
  schedule run <id>      queue a manual run
  schedule pause|resume <id>
  schedule rm <id>       remove a job
  schedule runs          recent run ledger
`, version)
}
