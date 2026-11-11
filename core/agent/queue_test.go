package agent

import (
	"testing"

	"github.com/uidbz/anneal/core/events"
)

func TestQueueOps(t *testing.T) {
	r := &Runner{bus: events.New()}
	r.queue = []QueuedInput{{ID: "i1", Text: "one"}, {ID: "i2", Text: "two"}}

	items, paused, _ := r.Queue()
	if len(items) != 2 || paused {
		t.Fatalf("queue = %+v paused=%v", items, paused)
	}
	if !r.Promote("i2") {
		t.Fatal("promote failed")
	}
	items, _, _ = r.Queue()
	if items[0].ID != "i2" || items[1].ID != "i1" {
		t.Fatalf("promote order = %+v", items)
	}
	if q, ok := r.CancelQueued("i1"); !ok || q.ID != "i1" {
		t.Fatalf("cancel = %+v %v", q, ok)
	}
	if dropped := r.ClearQueue(); len(dropped) != 1 || dropped[0].ID != "i2" {
		t.Fatalf("clear = %+v", dropped)
	}
	if items, _, _ := r.Queue(); len(items) != 0 {
		t.Fatalf("queue not empty: %+v", items)
	}
}

func TestQueuePausesAfterStop(t *testing.T) {
	r := &Runner{bus: events.New()}
	var paused map[string]any
	r.bus.AddTap(func(v any) {
		if m, ok := v.(map[string]any); ok && m["type"] == "queue.paused" {
			paused = m
		}
	})
	r.queue = []QueuedInput{{ID: "i1", Text: "next"}}
	r.startNextQueued(false) // run stopped/errored
	if !r.queuePaused {
		t.Fatal("queue did not pause")
	}
	if paused == nil || paused["reason"] != "previous run stopped" {
		t.Fatalf("paused event = %+v", paused)
	}
	if items, _, _ := r.Queue(); len(items) != 1 {
		t.Fatal("paused queue lost its input")
	}
}

func TestSubmitQueuesWhileBusy(t *testing.T) {
	r := &Runner{bus: events.New(), running: true, pendingPerms: map[string]*pendingPerm{}, doom: map[string]int{}}
	res, err := r.Submit("hello", "c1", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Queued || res.Position != 1 || res.Input.ID == "" {
		t.Fatalf("submit result = %+v", res)
	}
	items, _, _ := r.Queue()
	if len(items) != 1 || items[0].Text != "hello" || items[0].Ref != "c1" {
		t.Fatalf("queue = %+v", items)
	}
	if _, err := r.Submit("   ", "", ""); err == nil {
		t.Fatal("empty message accepted")
	}
}
