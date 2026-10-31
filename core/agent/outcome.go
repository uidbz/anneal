// Interrupted-turn reporting: dive records why a turn stopped and what
// happened to every tool call (completed / not started / unknown). anneal
// used to surface only "context deadline exceeded"; these helpers turn the
// record into a summary the UI can show with a Continue button.
package agent

import (
	"fmt"
	"strings"

	"github.com/deepnoodle-ai/dive"
)

// turnOutcomeEvent converts dive's turn outcome into the run.finished payload.
// A budget soft-cancel is reported as its own reason so it is not confused
// with the user pressing Stop.
func turnOutcomeEvent(resp *dive.Response, budget *turnBudget) map[string]any {
	var outcome *dive.TurnOutcome
	if resp != nil && resp.Turn != nil {
		outcome = resp.Turn.Outcome
	}
	if budget != nil && budget.isExceeded() {
		reason, next, errText := "budget", "continue", "the turn time budget was exhausted"
		if outcome != nil {
			return outcomePayload(reason, next, errText, outcome.ToolCalls)
		}
		return outcomePayload(reason, next, errText, nil)
	}
	if outcome == nil {
		return nil
	}
	return outcomePayload(string(outcome.Reason), string(outcome.Next), outcome.Error, outcome.ToolCalls)
}

func outcomePayload(reason, next, errText string, calls []dive.ToolCallRecord) map[string]any {
	return map[string]any{
		"reason":     reason,
		"next":       next,
		"error":      errText,
		"summary":    summarizeTurnOutcome(reason, calls),
		"tool_calls": calls,
	}
}

// summarizeTurnOutcome is the one-line explanation shown in the transcript
// (and used when a stored session is replayed).
func summarizeTurnOutcome(reason string, calls []dive.ToolCallRecord) string {
	label := outcomeLabel(reason)
	completed, notRun, unknown := 0, []string{}, []string{}
	for _, c := range calls {
		switch c.State {
		case dive.ToolCallStateCompleted:
			completed++
		case dive.ToolCallStateNotStarted:
			notRun = append(notRun, c.Name)
		case dive.ToolCallStateUnknown:
			unknown = append(unknown, c.Name)
		}
	}
	var sb strings.Builder
	sb.WriteString(label)
	if completed > 0 {
		fmt.Fprintf(&sb, " · %d step(s) completed", completed)
	}
	if len(notRun) > 0 {
		sb.WriteString(" · not run: " + joinNames(notRun))
	}
	if len(unknown) > 0 {
		sb.WriteString(" · unknown: " + joinNames(unknown) + " (check before continuing)")
	}
	return sb.String()
}

func joinNames(names []string) string {
	if len(names) > 3 {
		return strings.Join(names[:3], ", ") + fmt.Sprintf(" +%d", len(names)-3)
	}
	return strings.Join(names, ", ")
}

func outcomeLabel(reason string) string {
	switch reason {
	case "canceled":
		return "Stopped"
	case "deadline":
		return "Time limit reached"
	case "budget":
		return "Turn budget exhausted"
	case "provider_error":
		return "Model error"
	case "stream_interrupted":
		return "Model stream interrupted"
	case "hook_abort":
		return "Stopped by the application"
	case "callback_error":
		return "Internal error"
	case "output_limit":
		return "Output limit reached"
	case "context_limit":
		return "Context window reached"
	case "iteration_limit":
		return "Tool iteration limit reached"
	case "provider_stopped":
		return "Provider stopped early"
	case "process_exit":
		return "Interrupted (process ended)"
	case "pause":
		return "Paused"
	default:
		return "Turn failed"
	}
}
