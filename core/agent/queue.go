// Message queue: one foreground run at a time, but the user can keep typing.
// A message sent while a run is active is queued (never dropped) and starts
// as soon as the run finishes cleanly. A stop or error pauses the queue so
// the user decides what happens next; nothing is sent behind their back.
package agent

import (
	"fmt"
	"strings"
	"time"
)

// maxQueuedInputs bounds the queue. Input is cheap to hold, but an unbounded
// queue hides the fact that the agent is far behind.
const maxQueuedInputs = 20

// QueuedInput is one user message: running now, waiting, or paused.
type QueuedInput struct {
	ID   string    `json:"id"`
	Text string    `json:"text"`
	Ref  string    `json:"ref,omitempty"` // client correlation id
	Work string    `json:"work,omitempty"`
	At   time.Time `json:"at"`
}

// SubmitResult tells the caller what happened to one message.
type SubmitResult struct {
	Input    QueuedInput
	Run      int  // run id when it started immediately
	Queued   bool // true: waiting behind the active run
	Position int  // 1-based queue position when Queued
}

// Submit starts a run when idle, or queues the message. It replaces the old
// "busy => rejected" behaviour for clients that opt in. work is an optional
// prepared (absolute) work dir for the run, kept with the queued input.
func (r *Runner) Submit(text, ref, work string) (SubmitResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return SubmitResult{}, fmt.Errorf("message is empty")
	}
	r.mu.Lock()
	r.inputSeq++
	q := QueuedInput{ID: fmt.Sprintf("i%d", r.inputSeq), Text: text, Ref: ref, Work: work, At: time.Now()}
	r.mu.Unlock()

	if id, ok := r.StartTaskRef(q); ok {
		return SubmitResult{Input: q, Run: id}, nil
	}

	r.mu.Lock()
	if len(r.queue) >= maxQueuedInputs {
		r.mu.Unlock()
		return SubmitResult{Input: q}, fmt.Errorf("queue is full (%d)", maxQueuedInputs)
	}
	r.queue = append(r.queue, q)
	pos := len(r.queue)
	r.mu.Unlock()

	r.emit(map[string]any{"type": "input.queued", "input": q, "position": pos})
	return SubmitResult{Input: q, Queued: true, Position: pos}, nil
}

// Queue snapshots the waiting inputs (oldest first) and the pause state.
func (r *Runner) Queue() ([]QueuedInput, bool, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]QueuedInput(nil), r.queue...), r.queuePaused, r.queueReason
}

// CancelQueued removes one waiting input. A running input cannot be removed.
func (r *Runner) CancelQueued(id string) (QueuedInput, bool) {
	r.mu.Lock()
	for i, q := range r.queue {
		if q.ID != id {
			continue
		}
		r.queue = append(r.queue[:i], r.queue[i+1:]...)
		r.mu.Unlock()
		r.emit(map[string]any{"type": "input.cancelled", "input": q})
		return q, true
	}
	r.mu.Unlock()
	return QueuedInput{}, false
}

// ClearQueue drops every waiting input and returns them (the UI offers the
// text back to the composer).
func (r *Runner) ClearQueue() []QueuedInput {
	r.mu.Lock()
	dropped := append([]QueuedInput(nil), r.queue...)
	r.queue = nil
	r.mu.Unlock()
	if len(dropped) > 0 {
		r.emit(map[string]any{"type": "queue.cleared", "inputs": dropped})
	}
	return dropped
}

// Promote moves one waiting input to the front (the UI's "Run next").
func (r *Runner) Promote(id string) bool {
	r.mu.Lock()
	idx := -1
	for i, q := range r.queue {
		if q.ID == id {
			idx = i
			break
		}
	}
	if idx <= 0 {
		r.mu.Unlock()
		return idx == 0 // already next
	}
	q := r.queue[idx]
	copy(r.queue[1:idx+1], r.queue[:idx])
	r.queue[0] = q
	r.mu.Unlock()
	r.emit(map[string]any{"type": "queue.promoted", "input": q, "position": 1})
	return true
}

// ResumeQueue clears the pause and starts the next input when the runner is
// idle. While a run is active it only clears the pause: the queue continues
// when that run finishes.
func (r *Runner) ResumeQueue() {
	r.mu.Lock()
	r.queuePaused = false
	r.queueReason = ""
	idle := !r.running
	var next QueuedInput
	haveNext := false
	if idle && len(r.queue) > 0 {
		next = r.queue[0]
		r.queue = r.queue[1:]
		haveNext = true
	}
	count := len(r.queue)
	r.mu.Unlock()
	r.emit(map[string]any{"type": "queue.resumed", "count": count})
	if haveNext {
		r.emit(map[string]any{"type": "input.dequeued", "input": next})
		if _, ok := r.StartTaskRef(next); !ok {
			r.mu.Lock()
			r.queue = append([]QueuedInput{next}, r.queue...)
			r.mu.Unlock()
		}
	}
}

// startNextQueued is called after run.finished. A clean run continues the
// queue; a stop or an error pauses it.
func (r *Runner) startNextQueued(ok bool) {
	r.mu.Lock()
	if len(r.queue) == 0 {
		r.mu.Unlock()
		return
	}
	if !ok {
		if !r.queuePaused {
			r.queuePaused = true
			r.queueReason = "previous run stopped"
			n := len(r.queue)
			r.mu.Unlock()
			r.emit(map[string]any{"type": "queue.paused", "reason": r.queueReason, "count": n})
			return
		}
		r.mu.Unlock()
		return
	}
	next := r.queue[0]
	r.queue = r.queue[1:]
	r.mu.Unlock()

	r.emit(map[string]any{"type": "input.dequeued", "input": next})
	if _, started := r.StartTaskRef(next); !started {
		r.mu.Lock()
		r.queue = append([]QueuedInput{next}, r.queue...)
		r.queuePaused = true
		r.queueReason = "runner busy"
		r.mu.Unlock()
	}
}

// queueStateEvent is included in state replay so a reconnecting client shows
// the same tray.
func (r *Runner) queueStateEvent() map[string]any {
	items, paused, reason := r.Queue()
	return map[string]any{"type": "queue.state", "items": items, "paused": paused, "reason": reason}
}
