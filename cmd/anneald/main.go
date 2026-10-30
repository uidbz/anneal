// anneald: the anneal backend server. It hosts the core (agent runner,
// sessions, permissions, learning loop) behind the wire protocol
// (docs/PROTOCOL.md) so any client — the Fyne GUI in remote mode,
// anneal-headless -server, CI scripts — can drive it. Designed to run
// inside a container with one bind-mounted root (docs/CONTAINERS.md):
//
//	ANNEAL_HOME=/anneal/state anneald -work /anneal/work
//
// It exits cleanly on SIGTERM, on server.quit, or after idle_timeout_s
// without client activity — environments are ephemeral by design.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/uidbz/anneal/core/app"
	"github.com/uidbz/anneal/core/config"
	"github.com/uidbz/anneal/core/server"
)

// version is stamped with -X main.version at build time.
var version = "dev"

func main() {
	work := flag.String("work", "", "working directory override (default: server.work_dir config, else cwd)")
	listen := flag.String("listen", "", "comma-separated listen addresses override (unix:/path or host:port)")
	flag.Parse()

	// config.Load honors ANNEAL_HOME (state root) plus the ANNEAL_LISTEN /
	// ANNEAL_IDLE_TIMEOUT_S / ANNEAL_WORK_DIR container overrides. Flags win
	// over config: -listen is exported as ANNEAL_LISTEN so the config load
	// inside app.New sees it; the process then chdirs to the work dir before
	// app.New() so the runner picks it up as the project cwd.
	if *listen != "" {
		_ = os.Setenv("ANNEAL_LISTEN", *listen)
	}
	wd := *work
	if wd == "" {
		if cfg, err := config.Load(); err == nil {
			wd = cfg.Server.WorkDir
		}
	}
	if wd != "" {
		if err := os.MkdirAll(wd, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "anneald: work dir: %v\n", err)
			os.Exit(1)
		}
		if err := os.Chdir(wd); err != nil {
			fmt.Fprintf(os.Stderr, "anneald: work dir: %v\n", err)
			os.Exit(1)
		}
	}

	a, err := app.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "anneald:", err)
		os.Exit(1)
	}
	srv, err := server.New(a, version)
	if err != nil {
		fmt.Fprintln(os.Stderr, "anneald:", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	a.StartBlocking(ctx) // tie/MCP connect inline; monitor + review async
	if err := srv.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "anneald:", err)
		os.Exit(1)
	}
	reason := srv.Reason()
	if reason == "" {
		reason = "signal"
	}
	fmt.Fprintf(os.Stderr, "anneald: stopped (%s)\n", reason)
}
