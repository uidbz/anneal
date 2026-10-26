package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/uidbz/anneal/core/script"

	"github.com/deepnoodle-ai/dive"
)

const scriptDescription = "Run a Risor v2 script in-process for data glue — parse/convert JSON and CSV, reshape lists/maps, compute over files — instead of bash or python one-liners. Always available: json, csv, strings, filepath, time, and fs (fs.read, fs.write, fs.list, fs.exists, fs.remove; reads cover the project and ~/.anneal, writes only the project and ~/.anneal/scratch). No commands, network, or os module — use Bash for those. Risor v2 rules: no for/while loops (use .each/.map/.filter/.reduce), match arms must end with a _ => default arm, arrow functions like (x) => x * 2. The last expression is the result, returned as JSON; print() lines come back as output. 30s timeout."

// scriptTool exposes the sandboxed Risor script runner to the agent.
// No ReadOnlyHint: the tool can write files, so plan mode denies it and
// the permission manager asks by default (config agent.ask includes it).
func (r *Runner) scriptTool() dive.Tool {
	type scriptInput struct {
		Script  string   `json:"script" description:"Risor v2 source; the last expression is the result"`
		Modules []string `json:"modules,omitempty" description:"modules the script uses (subset of: json, csv, strings, filepath, time, fs)"`
	}
	return dive.FuncTool("script", scriptDescription,
		func(ctx context.Context, in *scriptInput) (*dive.ToolResult, error) {
			res, err := script.Run(ctx, script.Options{
				Script:  in.Script,
				Modules: in.Modules,
				Workdir: r.cwd,
			})
			var sb strings.Builder
			if res != nil && res.Output != "" {
				fmt.Fprintf(&sb, "output:\n%s", res.Output)
			}
			if err != nil {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(err.Error())
				return dive.NewToolResultText(sb.String()), nil
			}
			if res.ValueJSON != "" {
				fmt.Fprintf(&sb, "result: %s", res.ValueJSON)
			} else if sb.Len() == 0 {
				sb.WriteString("ok (no result)")
			}
			return dive.NewToolResultText(sb.String()), nil
		},
		dive.WithFuncToolAnnotations(&dive.ToolAnnotations{Title: "Script"}))
}
