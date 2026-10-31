// MCP integration: dive's experimental/mcp Manager owns the server
// connections (stdio or streamable HTTP); anneal connects them at startup,
// publishes their tools to the runner, and reports state to the UI.

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
	a.detectGortex()
	a.emitMCPState()
}

// detectGortex asks the gortex CLI which repositories it tracks. Its tools
// only work in tracked repositories: when the working directory is not one,
// they are omitted from runs (fewer prompt tokens, no repeated failures) and
// the UI offers to track the project.
func (a *App) detectGortex() {
	if _, ok := a.Cfg.MCP.Servers[gortexMCPName]; !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gortex", "repos", "--json").Output()
	if err != nil {
		return // best effort: keep tools rather than hide them on a probe error
	}
	var repos []struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(out, &repos) != nil {
		return
	}
	cwd := a.Runner.Cwd()
	tracked := make([]string, 0, len(repos))
	untracked := true
	for _, repo := range repos {
		if repo.Path == "" {
			continue
		}
		tracked = append(tracked, filepath.Clean(repo.Path))
		if pathWithin(cwd, repo.Path) {
			untracked = false
		}
	}
	a.Runner.SetGortexStatus(untracked, tracked)
	if untracked {
		a.Bus.Emit(map[string]any{"type": "gortex.untracked", "cwd": cwd, "tracked": tracked})
	}
}

// TrackGortex registers the current working directory with gortex (user
// action from the "Code intelligence off" card).
func (a *App) TrackGortex() error {
	cwd := a.Runner.Cwd()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gortex", "track", cwd).CombinedOutput()
	if err != nil {
		return fmt.Errorf("gortex track: %v: %s", err, strings.TrimSpace(string(out)))
	}
	a.detectGortex()
	a.Bus.Emit(map[string]any{"type": "gortex.tracked", "cwd": cwd})
	return nil
}

// pathWithin reports whether base is inside dir (or equal to it).
func pathWithin(base, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(base))
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
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
