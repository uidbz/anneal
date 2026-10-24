// MCP integration: dive's experimental/mcp Manager owns the server
// connections (stdio or streamable HTTP); fyagent connects them at startup,
// publishes their tools to the runner, and reports state to the UI.

package app

import (
	"context"
	"log"
	"sort"

	"fyagent/core/config"

	"github.com/deepnoodle-ai/dive/experimental/mcp"
)

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
	a.Bridge.Emit(map[string]any{"type": "mcp.state", "servers": out})
}

