package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/uidbz/anneal/core/permission"

	"github.com/deepnoodle-ai/dive"
)

// externalPath is one outside-the-project access that must be approved before
// the action itself, mirroring OpenCode's external_directory permission.
type externalPath struct {
	dir      string
	resource string
	save     string
}

type callIntent struct {
	action    string
	resources []string
	save      []string
	message   string
	readOnly  bool
	external  []externalPath
}

// permissionRequests turns one tool call into the ordered requests the hook
// evaluates: external directories first, then doom-loop, then the action.
func (r *Runner) permissionRequests(hctx *dive.HookContext) []permission.Request {
	tool := hctx.Tool
	if tool == nil {
		return nil
	}
	name := tool.Name()
	var raw json.RawMessage
	if hctx.Call != nil {
		raw = hctx.Call.Input
	}
	readOnly := false
	if ann := tool.Annotations(); ann != nil {
		readOnly = ann.ReadOnlyHint
	}

	intent := r.inspectCall(name, raw, readOnly)
	preview := previewJSON(raw, 300)

	var requests []permission.Request
	for _, ext := range intent.external {
		requests = append(requests, permission.Request{
			Action:    "external_directory",
			Resources: []string{ext.resource},
			Save:      []string{ext.save},
			Tool:      name,
			Message:   "Access " + ext.dir + " outside the working directory?",
			Preview:   preview,
			ReadOnly:  readOnly,
		})
	}
	if r.doomTripped(name, raw) {
		requests = append(requests, permission.Request{
			Action:    "doom_loop",
			Resources: []string{name},
			Save:      []string{"*"},
			Tool:      name,
			Message:   "The same tool call has repeated 3 times. Continue?",
			Preview:   preview,
			ReadOnly:  readOnly,
		})
	}
	if intent.action == "" {
		return requests
	}
	resources := intent.resources
	if len(resources) == 0 {
		resources = []string{permission.DefaultResource}
	}
	save := intent.save
	if len(save) == 0 {
		save = []string{permission.DefaultResource}
	}
	requests = append(requests, permission.Request{
		Action:    intent.action,
		Resources: resources,
		Save:      save,
		Tool:      name,
		Message:   intent.message,
		Preview:   preview,
		ReadOnly:  intent.readOnly,
	})
	return requests
}

// inspectCall maps a tool name + input to OpenCode's action/resources/save
// vocabulary. The mapping is intentionally explicit: a wrong guess about a
// tool's input would weaken a rule, so unknown tools fall back to an
// action-wide "*" resource and rely on their dive annotations for plan mode.
func (r *Runner) inspectCall(name string, raw json.RawMessage, readOnly bool) callIntent {
	in := decodeInput(raw)

	switch name {
	case "Read", "ReadFile":
		return r.pathIntent(field(in, "file_path", "filePath", "path"), "read", "Read a file?", true, false)
	case "ListDirectory":
		return r.pathIntent(field(in, "path"), "read", "List a directory?", true, true)
	case "Glob":
		pattern := field(in, "pattern")
		if pattern == "" {
			pattern = "*"
		}
		return callIntent{
			action:    "glob",
			resources: []string{pattern},
			save:      []string{"*"},
			message:   "Search file paths?",
			readOnly:  true,
			external:  r.externalPaths(field(in, "path"), true),
		}
	case "Grep":
		pattern := field(in, "pattern")
		if pattern == "" {
			pattern = "*"
		}
		return callIntent{
			action:    "grep",
			resources: []string{pattern},
			save:      []string{"*"},
			message:   "Search file contents?",
			readOnly:  true,
			external:  r.externalPaths(field(in, "path"), true),
		}
	case "Write", "WriteFile", "Edit":
		return r.pathIntent(field(in, "file_path", "filePath", "path"), "edit", "Modify a file?", false, false)
	case "TextEditor":
		command := strings.ToLower(field(in, "command"))
		if command == "view" {
			return r.pathIntent(field(in, "path"), "read", "View a file?", true, false)
		}
		return r.pathIntent(field(in, "path"), "edit", "Modify a file?", false, false)
	case "Bash":
		return r.bashIntent(field(in, "command"), field(in, "working_directory"))
	case "WebFetch":
		url := field(in, "url")
		return callIntent{
			action:    "webfetch",
			resources: []string{url},
			save:      []string{"*"},
			message:   "Fetch a URL?",
			readOnly:  true,
		}
	case "Skill":
		skill := field(in, "skill")
		return callIntent{
			action:    "skill",
			resources: []string{skill},
			save:      []string{skill},
			message:   "Load a skill?",
			readOnly:  true,
		}
	case "Agent":
		kind := field(in, "subagent_type")
		if kind == "" {
			kind = "general-purpose"
		}
		return callIntent{
			action:    "task",
			resources: []string{kind},
			save:      []string{"*"},
			message:   "Launch a " + kind + " subagent?",
		}
	case "todo":
		return callIntent{
			action:    "todowrite",
			resources: []string{"*"},
			save:      []string{"*"},
			message:   "Update the todo list?",
			readOnly:  true,
		}
	case "script":
		return callIntent{
			action:    "script",
			resources: []string{"*"},
			save:      []string{"*"},
			message:   "Run a sandboxed Risor script?",
		}
	case "memory":
		target := field(in, "target")
		if target == "" {
			target = "*"
		}
		return callIntent{
			action:    "memory",
			resources: []string{target},
			save:      []string{"*"},
			message:   "Use the memory store?",
			readOnly:  readOnly,
		}
	case "skill_manage":
		skill := field(in, "name")
		if skill == "" {
			skill = "*"
		}
		return callIntent{
			action:    "skill_manage",
			resources: []string{skill},
			save:      []string{"*"},
			message:   "Manage a skill?",
			readOnly:  readOnly,
		}
	case "session_search":
		return callIntent{
			action:    "session_search",
			resources: []string{"*"},
			save:      []string{"*"},
			message:   "Search past sessions?",
			readOnly:  true,
		}
	}

	// MCP tools and future tools: the action is the tool name. This is what
	// lets config write "mcp__gortex__explore": "allow".
	return callIntent{
		action:    permission.CanonicalAction(name),
		resources: []string{"*"},
		save:      []string{"*"},
		message:   "Run " + name + "?",
		readOnly:  readOnly,
	}
}

func (r *Runner) pathIntent(path, action, message string, readOnly, isDir bool) callIntent {
	resolved := r.resolvePath(path, isDir)
	intent := callIntent{
		action:    action,
		resources: []string{resolved.resource},
		save:      []string{"*"},
		message:   message,
		readOnly:  readOnly,
	}
	if resolved.external != nil {
		intent.external = append(intent.external, *resolved.external)
	}
	return intent
}

func (r *Runner) bashIntent(command, workdir string) callIntent {
	scan := permission.ScanCommand(command)
	intent := callIntent{action: "bash", message: "Run a shell command?"}
	for _, cmd := range scan.Commands {
		if cmd.Resource == "" {
			continue
		}
		intent.resources = append(intent.resources, cmd.Resource)
		if cmd.Save != "" {
			intent.save = append(intent.save, cmd.Save)
		}
	}

	base := r.cwd
	if workdir != "" {
		resolved := r.resolvePath(workdir, true)
		if resolved.external != nil {
			intent.external = append(intent.external, *resolved.external)
		}
		base = resolved.abs
	}
	for _, dir := range scan.Directories {
		abs := permission.ResolveDirectory(base, dir)
		if ext := r.externalForAbs(abs, true); ext != nil {
			intent.external = append(intent.external, *ext)
		}
		base = abs
	}

	// A command that only changes directory is covered by the external
	// approval; there is nothing to run.
	if len(intent.resources) == 0 {
		intent.action = ""
	}
	if len(intent.resources) > 0 && len(intent.save) == 0 {
		intent.save = []string{"*"}
	}
	return intent
}

// resolvedPath is an absolute path plus the relative (or absolute, when
// outside the project) resource OpenCode would report.
type resolvedPath struct {
	abs      string
	resource string
	external *externalPath
}

func (r *Runner) resolvePath(path string, isDir bool) resolvedPath {
	path = strings.TrimSpace(permission.ExpandHome(path))
	if path == "" {
		path = "."
	}
	base := r.baseDir()
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	abs := evalSymlinks(filepath.Clean(path))

	resource := filepath.ToSlash(abs)
	if rel, err := filepath.Rel(base, abs); err == nil && insideRel(rel) {
		resource = filepath.ToSlash(rel)
		if resource == "" {
			resource = "."
		}
	}
	return resolvedPath{abs: abs, resource: resource, external: r.externalForAbs(abs, isDir)}
}

func (r *Runner) externalPaths(path string, isDir bool) []externalPath {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	resolved := r.resolvePath(path, isDir)
	if resolved.external == nil {
		return nil
	}
	return []externalPath{*resolved.external}
}

func (r *Runner) externalForAbs(abs string, isDir bool) *externalPath {
	if abs == "" {
		return nil
	}
	base := r.baseDir()
	abs = evalSymlinks(abs)
	rel, err := filepath.Rel(base, abs)
	if err == nil && insideRel(rel) {
		return nil
	}

	if info, err := os.Stat(abs); err == nil {
		isDir = info.IsDir()
	}
	dir := abs
	if !isDir {
		dir = filepath.Dir(abs)
	}
	slash := filepath.ToSlash(dir)
	return &externalPath{dir: slash, resource: slash + "/*", save: slash + "/*"}
}

func (r *Runner) baseDir() string {
	if abs, err := filepath.Abs(r.cwd); err == nil {
		return evalSymlinks(filepath.Clean(abs))
	}
	return filepath.Clean(r.cwd)
}

func insideRel(rel string) bool {
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func evalSymlinks(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	if resolved, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		return filepath.Join(resolved, filepath.Base(path))
	}
	return path
}

func decodeInput(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var in map[string]any
	if err := json.Unmarshal(raw, &in); err != nil || in == nil {
		return map[string]any{}
	}
	return in
}

func field(in map[string]any, names ...string) string {
	for _, name := range names {
		if value, ok := in[name].(string); ok && value != "" {
			return value
		}
	}
	return ""
}
