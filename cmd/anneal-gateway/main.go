// anneal-gateway bridges anneald to messaging transports. It is deliberately
// a separate, small, CGO-free binary so it can run on a router while the agent
// runs on a build host.
//
//	anneal-gateway -config /etc/anneal/gateway.json
//
// The default config is ~/.anneal/gateway.json (ANNEAL_HOME aware). On first
// run a template is written and the process exits.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/uidbz/anneal/core/gateway"
	"github.com/uidbz/anneal/core/paths"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "", "gateway config path (default: $ANNEAL_HOME/gateway.json)")
	initOnly := flag.Bool("init", false, "write a default config and exit")
	flag.Parse()

	path := *configPath
	if path == "" {
		path = paths.GatewayConfig()
	}
	if *initOnly {
		if err := gateway.SaveConfig(path, gateway.DefaultConfig()); err != nil {
			fmt.Fprintln(os.Stderr, "anneal-gateway:", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "anneal-gateway: wrote %s\n", path)
		return
	}

	cfg, err := gateway.LoadConfig(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "anneal-gateway:", err)
		os.Exit(1)
	}
	g, err := gateway.New(*cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "anneal-gateway:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "anneal-gateway %s: config %s\n", version, path)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if err := g.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "anneal-gateway:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "anneal-gateway: stopped")
}
