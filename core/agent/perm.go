package agent

import (
	"context"
	"fmt"
	"strings"

	"fyagent/core/config"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/permission"
)

// uiDialog bridges dive permission prompts into the Risor UI as an approval
// inbox: each request is emitted as a perm.request event, and Show blocks
// (off the GUI thread) until the user answers via agent.respondPerm or the
// run is interrupted (which denies and closes the card).
type uiDialog struct {
	r *Runner
}

func (d *uiDialog) Show(ctx context.Context, in *dive.DialogInput) (*dive.DialogOutput, error) {
	if !in.Confirm {
		// fyagent's permission flow is yes/no only; anything else is denied.
		return &dive.DialogOutput{Canceled: true}, nil
	}
	tool := ""
	if in.Tool != nil {
		tool = in.Tool.Name()
	}
	// Plan mode must deny, not prompt. Ask rules run before dive's mode
	// gate, so enforce the read-only contract here.
	if d.r.mgr.Mode() == permission.ModePlan {
		ro := in.Tool != nil && in.Tool.Annotations() != nil && in.Tool.Annotations().ReadOnlyHint
		if !ro {
			return nil, fmt.Errorf("plan mode: %s denied (only read-only tools are allowed)", tool)
		}
	}
	input := ""
	if in.Call != nil {
		input = previewJSON(in.Call.Input, 300)
	}

	ch, id := d.r.registerPerm()
	d.r.emit(map[string]any{
		"type":    "perm.request",
		"id":      id,
		"tool":    tool,
		"message": in.Message,
		"grant":   d.r.mgr.SessionGrantLabel(in.Tool, in.Call),
		"input":   input,
	})

	select {
	case out := <-ch:
		return out, nil
	case <-ctx.Done():
		d.r.resolvePerm(id, "cancelled")
		return nil, fmt.Errorf("permission request cancelled (run interrupted)")
	}
}

// registerPerm creates a pending approval and returns its reply channel and id.
func (r *Runner) registerPerm() (chan *dive.DialogOutput, string) {
	r.mu.Lock()
	r.permSeq++
	id := fmt.Sprintf("p%d", r.permSeq)
	ch := make(chan *dive.DialogOutput, 1)
	r.pendingPerms[id] = ch
	r.mu.Unlock()
	return ch, id
}

// RespondPerm answers a pending approval: action is allow | session | deny.
func (r *Runner) RespondPerm(id, action string) {
	r.mu.Lock()
	ch, ok := r.pendingPerms[id]
	if ok {
		delete(r.pendingPerms, id)
	}
	r.mu.Unlock()
	if !ok {
		return
	}
	var out *dive.DialogOutput
	switch action {
	case "allow":
		out = &dive.DialogOutput{Confirmed: true}
	case "session":
		out = &dive.DialogOutput{AllowSession: true}
	default:
		out = &dive.DialogOutput{Canceled: true}
	}
	ch <- out
	r.resolvePerm(id, action)
}

// resolvePerm announces that a card is gone (answered, denied, or cancelled).
func (r *Runner) resolvePerm(id, reason string) {
	r.mu.Lock()
	delete(r.pendingPerms, id)
	r.mu.Unlock()
	r.emit(map[string]any{"type": "perm.resolved", "id": id, "reason": reason})
}

// SetMode changes the permission mode at runtime and persists it to config.
func (r *Runner) SetMode(mode string) {
	switch mode {
	case "default", "plan", "acceptEdits", "bypassPermissions", "dontAsk":
	default:
		return
	}
	r.mgr.SetMode(permission.Mode(mode))
	r.cfg.Agent.PermissionMode = mode
	_ = r.cfg.Save()
	r.emit(map[string]any{"type": "mode.changed", "mode": mode})
}

// Mode returns the active permission mode.
func (r *Runner) Mode() string { return string(r.mgr.Mode()) }

// permConfig translates fyagent config into a dive permission.Config.
// Rule strings look like "Bash" or "Bash(go test *)".
func permConfig(cfg *config.Config) *permission.Config {
	c := &permission.Config{Mode: permission.Mode(cfg.Agent.PermissionMode)}
	for _, s := range cfg.Agent.Allow {
		if s = strings.TrimSpace(s); s != "" {
			c.Rules = append(c.Rules, parseRule(permission.RuleAllow, s, ""))
		}
	}
	for _, s := range cfg.Agent.Ask {
		if s = strings.TrimSpace(s); s != "" {
			c.Rules = append(c.Rules, parseRule(permission.RuleAsk, s, ""))
		}
	}
	for _, s := range cfg.Agent.Deny {
		if s = strings.TrimSpace(s); s != "" {
			c.Rules = append(c.Rules, parseRule(permission.RuleDeny, s, "denied by config rule"))
		}
	}
	return c
}

func parseRule(typ permission.RuleType, spec, msg string) permission.Rule {
	tool, sp := spec, ""
	if i := strings.Index(spec, "("); i >= 0 && strings.HasSuffix(spec, ")") {
		tool, sp = spec[:i], spec[i+1:len(spec)-1]
	}
	return permission.Rule{Type: typ, Tool: tool, Specifier: sp, Message: msg}
}
