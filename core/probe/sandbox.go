// Package probe replays a task under a specific harness in an isolated
// scratch copy of the workspace (Harness²-style Probe step). The record it
// produces — e = (trajectory τ, deliverable y) — is the evidence unit for
// contrastive harness improvement (M12).
package probe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fyagent/core/config"
	"fyagent/core/paths"

	"github.com/deepnoodle-ai/dive"
)

// Sandbox is an isolated workspace copy plus the policy that jails a
// probe agent to it.
type Sandbox struct {
	Src  string `json:"src"`  // original directory (protected)
	Root string `json:"root"` // probe root dir
	WS   string `json:"ws"`   // workspace copy root
}

// SkipDirs are never copied into the sandbox (size; not needed to replay).
var SkipDirs = []string{"node_modules", ".venv", "venv", "__pycache__", ".next", "dist", "target", "out"}

// Prepare copies src into <root>/ws and returns the sandbox. An existing ws
// is removed first so a retried run can never observe files left behind by an
// earlier attempt.
func Prepare(src, root string) (*Sandbox, error) {
	src, err := filepath.Abs(src)
	if err != nil {
		return nil, err
	}
	ws := filepath.Join(root, "ws")
	if err := os.RemoveAll(ws); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return nil, err
	}
	s := &Sandbox{Src: src, Root: root, WS: ws}
	if err := s.copyTree(src, ws); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Sandbox) copyTree(src, ws string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			for _, skip := range SkipDirs {
				if d.Name() == skip {
					return filepath.SkipDir
				}
			}
			return os.MkdirAll(filepath.Join(ws, rel), 0o755)
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil // do not propagate symlinks into the sandbox
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(ws, rel), data, info.Mode().Perm())
	})
}

// inside reports whether abs path p is within dir.
func inside(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// absPath resolves a tool path arg; ok=false for relative/empty paths.
func absPath(v string) (string, bool) {
	if v == "" || !filepath.IsAbs(v) {
		return "", false
	}
	return filepath.Clean(v), true
}

// systemDirs are always readable (toolchain, libs).
var systemDirs = []string{"/bin", "/sbin", "/usr", "/lib", "/lib64", "/etc/ssl", "/etc/alternatives"}

func isSystemPath(p string) bool {
	for _, d := range systemDirs {
		if p == d || inside(p, d) {
			return true
		}
	}
	return p == "/dev/null" || p == "/proc/self/stat"
}

// jailDeniedPath reports whether a path arg is forbidden; msg explains. The
// sandbox is checked first: improvement probes live under the fyagent state
// directory (~/.fyagent/improvements/...), so the state-dir denial must not
// swallow the workspace itself.
func (s *Sandbox) jailDeniedPath(raw string, writeTool bool) (string, bool) {
	p, ok := absPath(raw)
	if !ok {
		return fmt.Sprintf("must be an absolute path under %s", s.WS), true
	}
	inWS := inside(p, s.WS)
	if writeTool && !inWS {
		return fmt.Sprintf("writes must stay inside %s", s.WS), true
	}
	if inWS {
		return "", false
	}
	if inside(p, paths.Root()) {
		return "fyagent state is off-limits to probes", true
	}
	if inside(p, s.Src) && p != s.Src {
		return "the original project is off-limits; use the sandbox copy", true
	}
	// reads: system dirs or elsewhere-outside-src are tolerated
	return "", false
}

// jailBash checks the bash tool: working_directory inside the sandbox, and
// no absolute-path tokens touching the original project or fyagent state.
func (s *Sandbox) jailBash(input map[string]any) (string, bool) {
	wd, _ := input["working_directory"].(string)
	wdAbs, ok := absPath(wd)
	if !ok {
		return fmt.Sprintf("set working_directory to an absolute path inside %s", s.WS), true
	}
	if !inside(wdAbs, s.WS) {
		return fmt.Sprintf("working_directory must be inside %s", s.WS), true
	}
	cmd, _ := input["command"].(string)
	for _, tok := range strings.Fields(cmd) {
		tok = strings.Trim(tok, "\"'`()$;{}<>&|")
		// Relative traversal escapes the absolute-path checks below
		// ("cd .. && cp … ../src"). The jail is defense-in-depth, not OS
		// isolation, but this specific hole defeats its stated promise.
		if tok == ".." || strings.HasPrefix(tok, "../") ||
			strings.HasSuffix(tok, "/..") || strings.Contains(tok, "/../") {
			return "command uses parent-directory traversal", true
		}
		if tok == "" || !strings.HasPrefix(tok, "/") {
			continue
		}
		// cut at glob/variable chars, check the concrete prefix
		if i := strings.IndexAny(tok, "*?"); i > 1 {
			tok = tok[:i]
		}
		if strings.ContainsAny(tok, "$") {
			continue
		}
		p := filepath.Clean(tok)
		if inside(p, s.WS) || isSystemPath(p) {
			continue
		}
		if inside(p, s.Src) || inside(s.Src, p) || inside(p, paths.Root()) || inside(paths.Root(), p) {
			return fmt.Sprintf("command touches protected path %q", p), true
		}
		// other absolute paths (neither sandbox, system, nor protected): allow —
		// they cannot corrupt the original project or fyagent state
	}
	return "", false
}

// pathKeys are the known file-path arguments of fyagent's toolkit tools.
var pathKeys = []string{"file_path", "path", "notebook_path", "directory"}

var writeTools = map[string]bool{"Write": true, "Edit": true, "MultiEdit": true, "TextEditor": true}

// Jail returns a PreToolUse hook enforcing the sandbox policy.
func (s *Sandbox) Jail() dive.PreToolUseHook {
	return func(_ context.Context, hctx *dive.HookContext) error {
		if hctx.Call == nil {
			return nil
		}
		var input map[string]any
		if len(hctx.Call.Input) > 0 {
			_ = json.Unmarshal(hctx.Call.Input, &input)
		}
		if input == nil {
			input = map[string]any{}
		}
		name := hctx.Call.Name
		if name == "Bash" {
			if msg, deny := s.jailBash(input); deny {
				return fmt.Errorf("probe jail: bash %s", msg)
			}
			return nil
		}
		writeTool := writeTools[name]
		for _, k := range pathKeys {
			v, ok := input[k].(string)
			if !ok || v == "" {
				continue
			}
			if msg, deny := s.jailDeniedPath(v, writeTool); deny {
				return fmt.Errorf("probe jail: %s %s=%q %s", name, k, v, msg)
			}
		}
		return nil
	}
}

// ConfigHash fingerprints the matched-execution conditions (model config +
// iteration budget) so probe contrasts are auditable.
func ConfigHash(cfg *config.Config, model string, maxIter int) string {
	doc := map[string]any{
		"endpoint": cfg.Provider.Endpoint, "model": model,
		"temperature": cfg.Provider.Temperature, "max_tokens": cfg.Provider.MaxTokens,
		"reasoning": cfg.Provider.ReasoningEffort, "max_iter": maxIter,
	}
	b, _ := json.Marshal(doc)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// HarnessHash fingerprints component files (path:content), "" layers equal.
func HarnessHash(layers []LayerContent) string {
	var parts []string
	for _, l := range layers {
		parts = append(parts, l.Name+"\x00"+l.Content)
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x01")))
	return hex.EncodeToString(sum[:8])
}

type LayerContent struct {
	Name    string
	Content string
}
