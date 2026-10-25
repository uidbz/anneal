package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"fyagent/core/paths"
	"fyagent/core/skills"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
	"github.com/deepnoodle-ai/dive/permission"
	"github.com/deepnoodle-ai/dive/skill"
	"github.com/deepnoodle-ai/dive/subagent"
	"github.com/deepnoodle-ai/dive/toolkit/orchestration"
)

// skillManageTool lets the agent author and refine skills (SKILL.md dirs
// under ~/.fyagent/skills, discovered by the dive skill loader next run).
func (r *Runner) skillManageTool() dive.Tool {
	type skillInput struct {
		Action      string `json:"action" description:"list | view | create | patch | write_file | remove"`
		Name        string `json:"name,omitempty" description:"skill name (lowercase slug)"`
		Description string `json:"description,omitempty" description:"create: when this skill should be used (one line)"`
		Content     string `json:"content,omitempty" description:"create: SKILL.md body; write_file: file content"`
		Path        string `json:"path,omitempty" description:"write_file: support file path inside the skill dir"`
		Old         string `json:"old,omitempty" description:"patch: unique substring to replace"`
		New         string `json:"new,omitempty" description:"patch: replacement text"`
		Reason      string `json:"reason,omitempty" description:"one-line justification (shown when staging)"`
	}
	return dive.FuncTool("skill_manage",
		"Author reusable skills — markdown playbooks auto-loaded into future sessions. Actions: list (see what exists), view (read SKILL.md), create (name+description+body), patch (unique-substring edit — prefer patching a skill when you learn something while using it), write_file (support file inside the skill), remove (archives the skill). Keep skills practical: steps, commands, gotchas.",
		func(ctx context.Context, in *skillInput) (*dive.ToolResult, error) {
			if r.skills == nil || !r.cfg.Skills.Enabled {
				return dive.NewToolResultText("skill_manage: disabled"), nil
			}
			switch in.Action {
			case "list":
				list := r.skills.List()
				if len(list) == 0 {
					return dive.NewToolResultText("skills: none installed."), nil
				}
				var sb strings.Builder
				fmt.Fprintf(&sb, "%d skills:\n", len(list))
				for _, it := range list {
					fmt.Fprintf(&sb, "- %s (%d chars): %s\n", it.Name, it.Chars, it.Description)
				}
				return dive.NewToolResultText(sb.String()), nil
			case "view":
				content, err := r.skills.View(in.Name)
				if err != nil {
					return dive.NewToolResultText("skill_manage: " + err.Error()), nil
				}
				return dive.NewToolResultText(content), nil
			}
			op := &skills.Op{
				Action: in.Action, Name: in.Name, Description: in.Description,
				Content: in.Content, Path: in.Path, Old: in.Old, New: in.New,
				Reason: in.Reason, Source: "agent",
			}
			if err := r.skills.Validate(op); err != nil {
				return dive.NewToolResultText("skill_manage: " + err.Error()), nil
			}
			if r.cfg.Skills.WriteApproval {
				staged, err := r.skills.Stage(op)
				if err != nil {
					return dive.NewToolResultText("skill_manage: " + err.Error()), nil
				}
				r.emit(map[string]any{"type": "skill.stage", "op": staged,
					"preview": r.skills.Preview(op)})
				return dive.NewToolResultText(fmt.Sprintf(
					"skill_manage: staged for user approval (id %s). NOT applied yet — the user sees the diff first.", staged.ID)), nil
			}
			result, err := r.skills.Apply(op)
			if err != nil {
				return dive.NewToolResultText("skill_manage: " + err.Error()), nil
			}
			r.emit(map[string]any{"type": "skill.applied", "action": op.Action, "name": op.Name})
			return dive.NewToolResultText("skill_manage: " + result), nil
		},
		// Gate is staging/write_approval (see memory tool reasoning).
		dive.WithFuncToolAnnotations(&dive.ToolAnnotations{ReadOnlyHint: true, Title: "Skill manager"}))
}

// skillLoader builds the dive skill loader for one run: fyagent-managed
// skills, project skills (./.fyagent/skills + .claude/.agents read-compat).
func (r *Runner) skillLoader() *skill.Loader {
	if !r.cfg.Skills.Enabled {
		return nil
	}
	extra := []string{paths.Skills(), filepath.Join(r.cwd, ".fyagent", "skills")}
	extra = append(extra, r.cfg.Skills.ExtraDirs...)
	loader, err := skill.Load(context.Background(), skill.LoaderOptions{
		ProjectDir:      r.cwd,
		AdditionalPaths: extra,
	})
	if err != nil {
		r.emit(map[string]any{"type": "skill.error", "error": err.Error()})
		return nil
	}
	return loader
}

// agentTool wires dive's subagent spawner with a factory that inherits
// fyagent's permission hooks (the default factory would spawn unguarded
// agents) and a bounded iteration limit.
func (r *Runner) agentTool(provider llm.LLM, tools []dive.Tool) dive.Tool {
	// explore/plan are research roles: keep them read-only w.r.t. fyagent's
	// own state too (no memory/skill writes).
	readOnly := func(def *subagent.Definition) *subagent.Definition {
		d := *def
		d.DisallowedTools = append(append([]string{}, def.DisallowedTools...),
			"memory", "skill_manage", "Skill")
		return &d
	}
	types := map[string]*subagent.Definition{
		"general-purpose": subagent.GeneralPurpose,
		"explore":         readOnly(subagent.Explore),
		"plan":            readOnly(subagent.Plan),
	}
	// Harness-defined subagents (M10): subagents/*.md override built-ins.
	if r.harn != nil && r.cfg.Harness.Enabled {
		defs, notes := r.harn.Subagents()
		for _, n := range notes {
			r.emit(map[string]any{"type": "harness.warning", "message": n})
		}
		for name, def := range defs {
			types[name] = def
		}
	}
	return orchestration.NewAgentTool(orchestration.AgentToolOptions{
		Subagents:   types,
		ParentTools: tools,
		AgentFactory: func(ctx context.Context, name string, def *subagent.Definition, parentTools []dive.Tool) (*dive.Agent, error) {
			return dive.NewAgent(dive.AgentOptions{
				Name:               name,
				SystemPrompt:       def.Prompt,
				Model:              provider,
				Tools:              subagent.FilterTools(def, parentTools),
				Hooks:              dive.Hooks{PreToolUse: []dive.PreToolUseHook{permission.HookFromManager(r.mgr)}},
				ToolIterationLimit: 40,
			})
		},
	})
}
