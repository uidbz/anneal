package agent

import (
	"context"
	"path/filepath"

	"github.com/uidbz/anneal/core/paths"
	"github.com/uidbz/anneal/core/skills"
	"github.com/uidbz/anneal/core/toolset"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
	"github.com/deepnoodle-ai/dive/skill"
	"github.com/deepnoodle-ai/dive/subagent"
	"github.com/deepnoodle-ai/dive/toolkit/orchestration"
)

// skillManageTool lets the agent author and refine skills (SKILL.md dirs
// under ~/.anneal/skills, discovered by the dive skill loader next run).
func (r *Runner) skillManageTool() dive.Tool {
	return toolset.SkillTool(r.skills, "agent", toolset.SkillCallbacks{
		Stage: func(op, staged *skills.Op) {
			r.emit(map[string]any{"type": "skill.stage", "op": staged,
				"preview": r.skills.Preview(op)})
		},
		Apply: func(op *skills.Op, _ string) {
			r.emit(map[string]any{"type": "skill.applied", "action": op.Action, "name": op.Name})
		},
	})
}

// skillLoader builds the dive skill loader for one run: anneal-managed
// skills, project skills (./.anneal/skills + .claude/.agents read-compat).
func (r *Runner) skillLoader() *skill.Loader {
	if !r.cfg.Skills.Enabled {
		return nil
	}
	extra := []string{
		paths.Skills(),
		filepath.Join(r.cwd, ".anneal", "skills"),
	}
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
// anneal's permission hooks (the default factory would spawn unguarded
// agents) and a bounded iteration limit.
func (r *Runner) agentTool(provider llm.LLM, tools []dive.Tool) dive.Tool {
	// explore/plan are research roles: keep them read-only w.r.t. anneal's
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
				Hooks:              dive.Hooks{PreToolUse: []dive.PreToolUseHook{r.permissionHook}},
				ToolIterationLimit: 40,
			})
		},
	})
}
