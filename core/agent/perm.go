package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/uidbz/anneal/core/config"
	"github.com/uidbz/anneal/core/harness"
	"github.com/uidbz/anneal/core/paths"
	"github.com/uidbz/anneal/core/permission"

	"github.com/deepnoodle-ai/dive"
)

// pendingPerm is one approval inbox card: the request being decided and the
// channel its blocked tool-call goroutine waits on.
type pendingPerm struct {
	req permission.Request
	ch  chan permission.Outcome
}

// permissionHook is the PreToolUse hook for the main agent and every spawned
// subagent. It mirrors OpenCode's Permission.assert: external-directory and
// doom-loop checks run first, then the action/resource check. Any deny is
// absolute; an ask becomes a perm.request card answered through RespondPerm.
func (r *Runner) permissionHook(ctx context.Context, hctx *dive.HookContext) error {
	if hctx == nil || hctx.Tool == nil {
		return nil
	}
	for _, req := range r.permissionRequests(hctx) {
		res := r.perm.Evaluate(req)
		switch res.Effect {
		case permission.Deny:
			return permissionError(req, res)
		case permission.Ask:
			outcome, err := r.askPermission(ctx, req)
			if err != nil {
				return err
			}
			if outcome == permission.Reject {
				return permissionError(req, permission.Result{Effect: permission.Deny, Reason: "rejected by user"})
			}
		}
	}
	return nil
}

func permissionError(req permission.Request, res permission.Result) error {
	subject := req.Action
	if len(req.Resources) > 0 {
		subject += " " + strings.Join(req.Resources, ", ")
	}
	if res.Reason != "" {
		return fmt.Errorf("permission denied: %s (%s)", subject, res.Reason)
	}
	return fmt.Errorf("permission denied: %s", subject)
}

// askPermission emits a perm.request event and blocks off the GUI thread until
// the user replies (or the run is interrupted, which denies the card).
func (r *Runner) askPermission(ctx context.Context, req permission.Request) (permission.Outcome, error) {
	id, ch := r.registerPerm(req)
	r.emit(map[string]any{
		"type":      "perm.request",
		"id":        id,
		"tool":      req.Tool,
		"action":    req.Action,
		"message":   req.Message,
		"grant":     grantLabel(req),
		"resources": req.Resources,
		"save":      req.Save,
		"input":     req.Preview,
	})

	select {
	case out := <-ch:
		return out, nil
	case <-ctx.Done():
		r.resolvePerm(id, "cancelled")
		return permission.Reject, fmt.Errorf("permission request cancelled (run interrupted)")
	}
}

func grantLabel(req permission.Request) string {
	if len(req.Save) == 0 {
		return ""
	}
	return strings.Join(req.Save, ", ")
}

// registerPerm creates a pending approval and returns its reply channel and id.
func (r *Runner) registerPerm(req permission.Request) (string, chan permission.Outcome) {
	r.permMu.Lock()
	if r.pendingPerms == nil {
		r.pendingPerms = make(map[string]*pendingPerm)
	}
	r.permSeq++
	id := fmt.Sprintf("p%d", r.permSeq)
	ch := make(chan permission.Outcome, 1)
	r.pendingPerms[id] = &pendingPerm{req: req, ch: ch}
	r.permMu.Unlock()
	return id, ch
}

// RespondPerm answers a pending approval. Accepted actions are
//
//	allow | once    approve just this request
//	session | always approve matching future requests (saved per project)
//	deny | reject   block the request
//
// The old allow/session/deny names remain for scripts and e2e drivers.
func (r *Runner) RespondPerm(id, action string) {
	r.permMu.Lock()
	p, ok := r.pendingPerms[id]
	if ok {
		delete(r.pendingPerms, id)
	}
	r.permMu.Unlock()
	if !ok {
		return
	}

	outcome := permission.Reject
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "allow", "once":
		outcome = permission.Once
	case "session", "always":
		outcome = permission.Always
		if len(p.req.Save) > 0 {
			if err := r.perm.AddSaved(p.req.Action, p.req.Save); err != nil {
				r.emit(map[string]any{"type": "perm.warning", "id": id, "error": err.Error()})
			} else {
				r.emit(map[string]any{"type": "perm.saved", "action": p.req.Action, "resources": p.req.Save})
			}
			r.autoAllowPending()
		}
	}

	p.ch <- outcome
	r.resolvePerm(id, action)
}

// autoAllowPending releases pending cards made unnecessary by an "always"
// grant, exactly like OpenCode re-evaluating its pending requests after a save.
func (r *Runner) autoAllowPending() {
	type ready struct {
		id string
		ch chan permission.Outcome
	}
	var allow []ready

	r.permMu.Lock()
	for id, p := range r.pendingPerms {
		if r.perm.Evaluate(p.req).Effect != permission.Allow {
			continue
		}
		delete(r.pendingPerms, id)
		allow = append(allow, ready{id: id, ch: p.ch})
	}
	r.permMu.Unlock()

	for _, item := range allow {
		item.ch <- permission.Once
		r.emit(map[string]any{"type": "perm.resolved", "id": item.id, "reason": "always"})
	}
}

// resolvePerm announces that a card is gone (answered, denied, or cancelled).
func (r *Runner) resolvePerm(id, reason string) {
	r.permMu.Lock()
	delete(r.pendingPerms, id)
	r.permMu.Unlock()
	r.emit(map[string]any{"type": "perm.resolved", "id": id, "reason": reason})
}

// SetMode changes the permission mode at runtime and persists it to config.
func (r *Runner) SetMode(mode string) {
	if !permission.ValidMode(mode) {
		return
	}
	if r.Mode() == mode {
		return
	}
	r.perm.SetMode(mode)
	r.cfg.Agent.PermissionMode = mode
	_ = r.cfg.Save()
	r.emit(map[string]any{"type": "mode.changed", "mode": mode})
}

// Mode returns the active permission mode.
func (r *Runner) Mode() string { return string(r.perm.Mode()) }

// resetDoom clears repeated-call detection at the start of a run.
func (r *Runner) resetDoom() {
	r.permMu.Lock()
	r.doom = make(map[string]int)
	r.permMu.Unlock()
}

// doomTripped counts identical calls and reports every third occurrence. The
// doom_loop permission action defaults to ask, matching OpenCode.
func (r *Runner) doomTripped(tool string, input []byte) bool {
	key := tool + "\x00" + string(input)
	r.permMu.Lock()
	if len(r.doom) > 2048 {
		r.doom = make(map[string]int)
	}
	if r.doom == nil {
		r.doom = make(map[string]int)
	}
	r.doom[key]++
	n := r.doom[key]
	r.permMu.Unlock()
	return n >= 3 && n%3 == 0
}

// permConfig translates config + harness guardrails into one ordered ruleset.
// Order (later wins): legacy allow/ask/deny, built-in .env guard, global
// permission/permissions, agent permission/permissions, harness guardrails.
func permConfig(cfg *config.Config, harn *harness.Store) permission.Rules {
	var rules permission.Rules
	rules = append(rules, permission.ParseRules(permission.Allow, cfg.Agent.Allow)...)
	rules = append(rules, permission.ParseRules(permission.Ask, cfg.Agent.Ask)...)
	rules = append(rules, permission.ParseRules(permission.Deny, cfg.Agent.Deny)...)
	// Built-in secret-file guard, after the legacy read allow so it wins by
	// default; explicit config rules below can override it.
	rules = append(rules, permission.EnvReadRules()...)
	rules = append(rules, cfg.Permission...)
	rules = append(rules, cfg.Permissions...)
	rules = append(rules, cfg.Agent.Permission...)
	rules = append(rules, cfg.Agent.Permissions...)
	if harn != nil && cfg.Harness.Enabled {
		allow, ask, deny := harn.GuardRules()
		rules = append(rules, permission.ParseRules(permission.Allow, allow)...)
		rules = append(rules, permission.ParseRules(permission.Ask, ask)...)
		rules = append(rules, permission.ParseRules(permission.Deny, deny)...)
	}
	return rules
}

func newPermManager(cfg *config.Config, harn *harness.Store, cwd string) *permission.Manager {
	return permission.NewManager(permission.Options{
		Mode:      cfg.Agent.PermissionMode,
		Rules:     permConfig(cfg, harn),
		Project:   cwd,
		SavedPath: paths.Permissions(),
	})
}
