// Package toolset provides the model-facing memory and skill tools shared by
// the foreground agent and the background reviewer.
//
// The stores own validation, staging, and persistence. Callers differ only in
// bookkeeping: the agent emits UI events, the reviewer records ledger entries.
// Keeping one implementation means both paths enforce the same caps, injection
// scanner, dup-guard, and write-approval semantics.
package toolset

import (
	"context"
	"fmt"
	"strings"

	"github.com/uidbz/anneal/core/memory"
	"github.com/uidbz/anneal/core/skills"

	"github.com/deepnoodle-ai/dive"
)

// MemoryCallbacks lets a caller observe memory operations without owning the
// tool logic. requested is the caller's op; staged is what the store persisted
// (with ID/TS/Source filled in). Either callback may be nil.
type MemoryCallbacks struct {
	Stage func(requested, staged *memory.Op)
	Apply func(op *memory.Op, content string)
}

const memoryDescription = "Save durable knowledge: add/patch/remove entries in two small bounded stores — target 'memory' (facts, conventions, environment quirks) and 'user' (preferences, habits). Entries re-enter every future session, so keep them short, factual and current (patch/remove stale ones). Do NOT store conversation play-by-play; use session_search for past conversations."

// MemoryTool builds the memory tool for one caller. source is stamped on the
// op ("agent" or "review") for approval cards and auditability.
func MemoryTool(store *memory.Store, source string, cb MemoryCallbacks) dive.Tool {
	type memInput struct {
		Target  string `json:"target" description:"'memory' for project/world facts and conventions, 'user' for the user's preferences"`
		Action  string `json:"action" description:"add | patch | remove"`
		Content string `json:"content,omitempty" description:"entry text for add"`
		Old     string `json:"old,omitempty" description:"unique substring identifying the text to patch/remove"`
		New     string `json:"new,omitempty" description:"replacement text for patch"`
		Reason  string `json:"reason,omitempty" description:"one-line justification (shown to the user when staging)"`
	}
	return dive.FuncTool("memory", memoryDescription,
		func(ctx context.Context, in *memInput) (*dive.ToolResult, error) {
			op := &memory.Op{
				Target: in.Target, Action: in.Action,
				Content: in.Content, Old: in.Old, New: in.New,
				Reason: in.Reason, Source: source,
			}
			if err := store.Validate(op); err != nil {
				return dive.NewToolResultText("memory: " + err.Error()), nil
			}
			if store.WriteApproval() {
				staged, err := store.Stage(op)
				if err != nil {
					return dive.NewToolResultText("memory: " + err.Error()), nil
				}
				if cb.Stage != nil {
					cb.Stage(op, staged)
				}
				return dive.NewToolResultText(fmt.Sprintf(
					"memory: staged for user approval (id %s). It is NOT saved yet — the user must approve it.", staged.ID)), nil
			}
			content, err := store.Apply(op)
			if err != nil {
				return dive.NewToolResultText("memory: " + err.Error()), nil
			}
			if cb.Apply != nil {
				cb.Apply(op, content)
			}
			return dive.NewToolResultText(fmt.Sprintf(
				"memory: %s ok — %s now %d/%d chars.", op.Action, op.Target,
				len(content), store.Limit(op.Target))), nil
		},
		// Read-only hint keeps planning (plan mode) possible; the actual
		// write gate is write_approval staging, not the permission manager.
		dive.WithFuncToolAnnotations(&dive.ToolAnnotations{ReadOnlyHint: true, Title: "Memory"}))
}

// SkillCallbacks is the skill-tool analogue of MemoryCallbacks.
type SkillCallbacks struct {
	Stage func(requested, staged *skills.Op)
	Apply func(op *skills.Op, result string)
}

const skillDescription = "Author reusable skills — markdown playbooks auto-loaded into future sessions. Actions: list (see what exists), view (read SKILL.md), create (name+description+body), patch (unique-substring edit — prefer patching a skill when you learn something while using it), write_file (support file inside the skill), remove (archives the skill). Keep skills practical: steps, commands, gotchas."

// SkillTool builds the skill_manage tool for one caller.
func SkillTool(store *skills.Store, source string, cb SkillCallbacks) dive.Tool {
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
	return dive.FuncTool("skill_manage", skillDescription,
		func(ctx context.Context, in *skillInput) (*dive.ToolResult, error) {
			switch in.Action {
			case "list":
				list := store.List()
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
				content, err := store.View(in.Name)
				if err != nil {
					return dive.NewToolResultText("skill_manage: " + err.Error()), nil
				}
				return dive.NewToolResultText(content), nil
			}
			op := &skills.Op{
				Action: in.Action, Name: in.Name, Description: in.Description,
				Content: in.Content, Path: in.Path, Old: in.Old, New: in.New,
				Reason: in.Reason, Source: source,
			}
			if err := store.Validate(op); err != nil {
				return dive.NewToolResultText("skill_manage: " + err.Error()), nil
			}
			if store.WriteApproval() {
				staged, err := store.Stage(op)
				if err != nil {
					return dive.NewToolResultText("skill_manage: " + err.Error()), nil
				}
				if cb.Stage != nil {
					cb.Stage(op, staged)
				}
				return dive.NewToolResultText(fmt.Sprintf(
					"skill_manage: staged for user approval (id %s). NOT applied yet — the user sees the diff first.", staged.ID)), nil
			}
			result, err := store.Apply(op)
			if err != nil {
				return dive.NewToolResultText("skill_manage: " + err.Error()), nil
			}
			if cb.Apply != nil {
				cb.Apply(op, result)
			}
			return dive.NewToolResultText("skill_manage: " + result), nil
		},
		// Gate is staging/write_approval (see memory tool reasoning).
		dive.WithFuncToolAnnotations(&dive.ToolAnnotations{ReadOnlyHint: true, Title: "Skill manager"}))
}
