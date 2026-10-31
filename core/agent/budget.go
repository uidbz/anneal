// Active turn budget: model and tool time, but not time the user spends
// deciding an approval. dive's built-in ResponseTimeout counts wall time, so
// a long approval wait could kill a turn mid-write; this budget is the real
// control and the dive deadline is only a backstop.
package agent

import (
	"context"
	"sync"
	"time"

	"github.com/deepnoodle-ai/dive"
)

// turnBudget is shared by the watchdog goroutine and the PreIteration hook.
type turnBudget struct {
	mu       sync.Mutex
	warn     bool
	final    bool
	exceeded bool
}

func (b *turnBudget) setWarn() {
	b.mu.Lock()
	b.warn = true
	b.mu.Unlock()
}

func (b *turnBudget) setFinal() {
	b.mu.Lock()
	b.final = true
	b.mu.Unlock()
}

func (b *turnBudget) setExceeded() {
	b.mu.Lock()
	b.exceeded = true
	b.mu.Unlock()
}

func (b *turnBudget) isExceeded() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.exceeded
}

// hook delivers the warning/final reminders at the next model call and makes
// the final call tool-free.
func (b *turnBudget) hook() dive.PreIterationHook {
	return func(_ context.Context, hctx *dive.HookContext) error {
		b.mu.Lock()
		warn, final := b.warn, b.final
		if warn {
			b.warn = false
		}
		b.mu.Unlock()
		if final {
			hctx.FinalCall = true
			if rem, err := dive.NewContextReminder("turn-budget",
				"The turn time budget is exhausted. Do not start new work. "+
					"Summarize concisely: what is done, what is left, and which files changed, "+
					"so the next turn can continue from your summary."); err == nil {
				_ = hctx.AppendReminder(rem, dive.ModelOnly)
			}
			return nil
		}
		if warn {
			if rem, err := dive.NewContextReminder("turn-budget",
				"About 80% of the turn time budget is used. Finish the current step, "+
					"update the todo list, and checkpoint what is done and what remains."); err == nil {
				_ = hctx.AppendReminder(rem, dive.ModelOnly)
			}
		}
		return nil
	}
}

// watchTurnBudget polls active time (wall time minus approval waits) and fires
// the warning/exhaustion thresholds. It returns a stop function for the timer.
func (r *Runner) watchTurnBudget(ctx context.Context, started time.Time, budget time.Duration, tb *turnBudget, softCancel func()) func() {
	stop := make(chan struct{})
	if budget <= 0 {
		return func() { close(stop) }
	}
	warnAt := budget * 8 / 10
	go func() {
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		warned, final := false, false
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				active := time.Since(started) - r.currentPermWait()
				if !warned && active >= warnAt {
					warned = true
					tb.setWarn()
					r.emit(map[string]any{"type": "turn.budget", "phase": "warning",
						"used_s": int(active.Seconds()), "budget_s": int(budget.Seconds())})
				}
				if !final && active >= budget {
					final = true
					tb.setFinal()
					r.emit(map[string]any{"type": "turn.budget", "phase": "exhausted",
						"used_s": int(active.Seconds()), "budget_s": int(budget.Seconds())})
					// Grace: a runaway tool call must not outlive the budget
					// forever. The context check makes this a no-op once the
					// run has ended (its cancel runs before this fires).
					time.AfterFunc(3*time.Minute, func() {
						if ctx.Err() != nil {
							return
						}
						tb.setExceeded()
						if softCancel != nil {
							softCancel()
						}
					})
				}
			}
		}
	}()
	return func() { close(stop) }
}

// currentPermWait is cumulative approval wait for the active run.
func (r *Runner) currentPermWait() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.permWait
}
