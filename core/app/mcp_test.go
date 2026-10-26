package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/uidbz/anneal/core/config"
)

func writeFakeGortex(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gortex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return path
}

func TestRegisterBuiltinMCPGortex(t *testing.T) {
	writeFakeGortex(t)
	cfg := config.Default()
	a := &App{Cfg: cfg}
	a.registerBuiltinMCP()

	srv, ok := cfg.MCP.Servers[gortexMCPName]
	if !ok {
		t.Fatal("gortex server not registered")
	}
	if srv.Command != "gortex" || len(srv.Args) != 1 || srv.Args[0] != "mcp" {
		t.Fatalf("unexpected server: %+v", srv)
	}
	if srv.Env["GORTEX_TOOLS"] != "localization" {
		t.Fatalf("preset: %+v", srv.Env)
	}
	found := false
	for _, rule := range cfg.Agent.Allow {
		if rule == "mcp__gortex__explore" {
			found = true
		}
		if rule == "mcp__gortex__*" {
			t.Fatal("blanket gortex allow rule is unsafe")
		}
	}
	if !found {
		t.Fatalf("gortex tools not allowlisted: %v", cfg.Agent.Allow)
	}
}

func TestRegisterBuiltinMCPRespectsConfigAndMissingBinary(t *testing.T) {
	cfg := config.Default()
	cfg.MCP.AutoDetect = false
	a := &App{Cfg: cfg}
	a.registerBuiltinMCP()
	if _, ok := cfg.MCP.Servers[gortexMCPName]; ok {
		t.Fatal("auto_detect=false still registered gortex")
	}

	t.Setenv("PATH", t.TempDir()) // no gortex here
	cfg = config.Default()
	cfg.MCP.Servers[gortexMCPName] = config.MCPServer{Command: "custom"}
	a = &App{Cfg: cfg}
	a.registerBuiltinMCP()
	if got := cfg.MCP.Servers[gortexMCPName].Command; got != "custom" {
		t.Fatalf("user config overwritten: %q", got)
	}
}
