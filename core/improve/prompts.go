package improve

import "fmt"

// Prompts are condensed ports of Harness²'s Appendix H stage prompts,
// tuned for a small local model: short imperative checklists, evidence
// quoting, no nested abstraction.

const componentRules = `You work in a directory holding the editable harness component files
(this run they are already copied for you):
- system.md        workstyle rules appended to fyagent's system prompt
- guardrails.md    behavioral guardrails; lines "deny: Tool(spec)", "ask: ...",
                   "allow: ..." are machine-enforced permission rules
- tool-guidance.md extra instructions on using fyagent's tools well
- subagents/<name>.md subagent definitions (frontmatter: description,
  model, tools; the key is the file name)
Do NOT invent other files (there is no scripts.md component).
Edit them with the file tools using their ABSOLUTE paths (the task text
tells you the directory, e.g. Write file_path="<dir>/system.md"). Keep
every file under 3500 characters.
Never store secrets or task play-by-play. Never reference fyagent's own
state directory paths.`

func editorSystem(kind string) string {
	role := `You are fyagent's INVARIANT harness editor.
Improve the harness for AGENTIC deficiencies that recur across any task:
- planning: work started without a checklist; "done" declared unverified
- verification: produced files never re-opened; tests/builds not re-run
- tool use: repeated failing commands; missing cwd/absolute paths
- delegation: research bundled into the main thread
Read the reference execution FIRST and list observed deficiencies, quoting
the exact line that shows each. Then edit the component files so a future
fyagent run avoids them.
Rules:
- ` + componentRules + `
- NO task-specific facts: no file names, values, or domain details from
  this task belong in your edits.
- Prefer one imperative line per rule. Keep total edits small (<= 25 lines).
When done, reply with 3 short lines: what you changed and which observed
deficiency each change targets.`
	if kind == "domain" {
		role = `You are fyagent's DOMAIN harness editor.
Improve the harness for THIS task's domain: the procedures, constraints,
formats, and verification steps the reference execution did not know.
Read the reference execution FIRST and list, with quotes, the domain
missteps: wrong procedures, missed checks, malformed outputs, ignored
constraints. Then edit the component files to carry that knowledge.
Rules:
- ` + componentRules + `
- Ground every claim in the transcript: only encode procedures/constraints
  the reference execution actually touched. No guesses, no generic advice.
- Prefer one imperative line per rule. Keep total edits small (<= 25 lines).
When done, reply with 3 short lines: what you changed and which observed
misstep each change targets.`
	}
	return role
}

func editorTask(task, refDigest, dir, lib string) string {
	out := fmt.Sprintf(`Task that was executed:
%s

Reference execution (what actually happened, condensed):
%s
`, clip(task, 1200), refDigest)
	if lib != "" {
		out += "\nPRIOR HARNESS IMPROVEMENT LIBRARY (reuse or adapt adopted\ncomponents and lessons; verify any task-specific fact against THIS task):\n" + lib + "\n"
	}
	out += fmt.Sprintf(`
Your harness directory is %s — edit its files with ABSOLUTE paths
(e.g. file_path="%s/system.md"). Inspect the deficiencies, then edit.
Finish with the 3-line summary.`, dir, dir)
	return out
}

func contrastPrompt(kind, task, refDigest, diff, probeDigest string) string {
	return fmt.Sprintf(`You compare two executions of the same task under different harnesses.
No ground truth exists: judge BEHAVIOR, not correctness.

Task:
%s

REFERENCE execution (old harness):
%s

HARNESS CHANGES (%s editor):
%s

PROBE execution (with those changes):
%s

Write a CONTRAST NOTE (max 25 lines):
- per intended change: improved | unchanged | regressed, with one quoted
  observation from each side;
- any NEW problems the probe introduced;
- one final line: "keep", "adjust", or "drop" the change set and why.
No scores, no praise.`, clip(task, 800), refDigest, kind, clip(diff, 3000), probeDigest)
}

const composerSystem = `You are fyagent's harness COMPOSER. You get the reference execution, both
candidate harness change sets, and their contrast notes. Compose the
SUCCESSOR harness in your working directory (a copy of the current harness
is already there — edit it in place).
Rules:
- ` + componentRules + `
- Be SELECTIVE: adopt only changes with observed support; merge overlapping
  edits into one; rewrite over-broad rules; DROP unsupported or risky ones.
- Preserve the current harness where evidence is missing.
- MANDATORY: finish by writing decisions.md — one line per candidate
  change: "<file>: adopt|rewrite|drop — <observation from the contrast
  note or reference>". Every decision must cite an observation.
Keep the successor small: at most 60 new lines total across files.`

func composerTask(task, refDigest string, blocks string, dir, lib string) string {
	out := fmt.Sprintf(`Task:
%s

REFERENCE execution (old harness):
%s
`, clip(task, 800), refDigest)
	if lib != "" {
		out += "\nPRIOR HARNESS IMPROVEMENT LIBRARY (prefer its proven components over\nreinventing the same rule):\n" + lib + "\n"
	}
	out += "\n" + blocks + fmt.Sprintf(`

Your successor directory is %s — edit its files with ABSOLUTE paths
(e.g. file_path="%s/system.md"), then write decisions.md, then reply
"composed".`, dir, dir)
	return out
}

// selectorTask presents anonymized parallel-candidate outcomes; the
// iteration identity is deliberately absent.
func selectorTask(task, refDigest string, blocks string) string {
	return fmt.Sprintf(`The same task was run under several candidate harness improvements.
Pick which final outcome to adopt, by task fit and observed behavior
(verified work beats claims; shorter beats longer when equal).

TASK:
%s

REFERENCE (old harness):
%s

%s

Answer with exactly one letter (A-H) or NONE if none is clearly better.`,
		clip(task, 800), clip(refDigest, 2500), blocks)
}

func reflectorTask(task, decisions, contrasts, refHead, succHead string) string {
	return fmt.Sprintf(`Review this completed harness improvement step and distill it for the library.

TASK: %s

DECISIONS (composer):
%s

CONTRAST NOTES:
%s

REFERENCE (head):
%s

SUCCESSOR RUN (head):
%s

Write an EXPERIENCE entry, max 22 lines, exactly this shape:
## situation
<one line: kind of task + the deficiency class addressed>
## lessons
- <change → observed effect> (only changes supported by decisions/contrasts)
## evidence
- <at most 4 quoted observations>

If no lesson is supported, reply NONE.`, clip(task, 500), clip(decisions, 2500),
		clip(contrasts, 2500), clip(refHead, 2000), clip(succHead, 2000))
}
