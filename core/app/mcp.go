// MCP integration: dive's experimental/mcp Manager owns the server
// connections (stdio or streamable HTTP); anneal connects them at startup,
// publishes their tools to the runner, and reports state to the UI.

package app

import (
	"context"
	"log"
	"os/exec"
	"sort"

	"github.com/uidbz/anneal/core/config"

	"github.com/deepnoodle-ai/dive/experimental/mcp"
)

// gortexMCPName is the built-in server name for zzet/gortex, a local
// code-intelligence MCP server. Auto-detected when the binary is on PATH.
const gortexMCPName = "gortex"

// registerBuiltinMCP adds well-known local MCP servers when their binaries
// are installed. Gortex runs its read-only "localization" preset (navigation
// and symbol search), and the matching tools are allowlisted so analysis does
// not prompt on every lookup. Existing user configuration always wins.
func (a *App) registerBuiltinMCP() {
	if !a.Cfg.MCP.Enabled || !a.Cfg.MCP.AutoDetect {
		return
	}
	if _, ok := a.Cfg.MCP.Servers[gortexMCPName]; ok {
		return
	}
	if _, err := exec.LookPath("gortex"); err != nil {
		return
	}
	if a.Cfg.MCP.Servers == nil {
		a.Cfg.MCP.Servers = map[string]config.MCPServer{}
	}
	a.Cfg.MCP.Servers[gortexMCPName] = config.MCPServer{
		Command: "gortex",
		Args:    []string{"mcp"},
		Env:     map[string]string{"GORTEX_TOOLS": "localization"},
	}
	// Allow only the read-only localization tools (plus catalog discovery).
	// tools_search can promote deferred tools, so a blanket mcp__gortex__*
	// rule could silently allow a mutating tool later.
	for _, tool := range []string{"explore", "batch_symbols", "find_implementations", "tool_profile", "tools_search"} {
		a.Cfg.Agent.Allow = append(a.Cfg.Agent.Allow, "mcp__gortex__"+tool)
	}
}

func toServerConfigs(servers map[string]config.MCPServer) []*mcp.ServerConfig {
	var out []*mcp.ServerConfig
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		s := servers[n]
		if s.Disabled || (s.Command == "" && s.URL == "") {
			continue
		}
		typ := s.Type
		if typ == "" {
			typ = "stdio"
			if s.Command == "" {
				typ = "http"
			}
		}
		out = append(out, &mcp.ServerConfig{
			Name: n, Type: typ, Command: s.Command, Args: s.Args,
			Env: s.Env, URL: s.URL,
		})
	}
	return out
}

// ConnectMCP initializes configured MCP servers (blocking; call off the GUI
// path) and emits an mcp.state event with per-server connection status.
func (a *App) ConnectMCP(ctx context.Context) {
	if !a.Cfg.MCP.Enabled || len(a.Cfg.MCP.Servers) == 0 {
		return
	}
	mgr := mcp.NewManager()
	if err := mgr.InitializeServers(ctx, toServerConfigs(a.Cfg.MCP.Servers)); err != nil {
		log.Printf("mcp: initialize: %v", err)
	}
	a.MCP = mgr
	a.Runner.SetMCP(mgr)
	a.emitMCPState()
}

func (a *App) emitMCPState() {
	if a.MCP == nil {
		return
	}
	status := a.MCP.GetServerStatus()
	names := a.MCP.GetServerNames()
	sort.Strings(names)
	type srv struct {
		Name      string `json:"name"`
		Connected bool   `json:"connected"`
		Tools     int    `json:"tools"`
	}
	out := make([]srv, 0, len(names))
	for _, n := range names {
		out = append(out, srv{Name: n, Connected: status[n], Tools: len(a.MCP.GetToolsByServer(n))})
	}
	a.Bus.Emit(map[string]any{"type": "mcp.state", "servers": out})
}
