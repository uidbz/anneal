package agent

import "github.com/deepnoodle-ai/dive"

// sequentialTool marks a tool as batch-serialising. dive runs a whole batch
// sequentially when any call in it carries SequentialOnlyHint, so a mixed
// read+write batch stays ordered while a pure read-only batch can run in
// parallel.
type sequentialTool struct{ dive.Tool }

func (s sequentialTool) Annotations() *dive.ToolAnnotations {
	base := s.Tool.Annotations()
	out := dive.ToolAnnotations{}
	if base != nil {
		out = *base
	}
	out.SequentialOnlyHint = true
	return &out
}

func seq(t dive.Tool) dive.Tool { return sequentialTool{Tool: t} }
