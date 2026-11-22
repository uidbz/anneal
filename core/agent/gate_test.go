package agent

import (
	"context"
	"testing"
	"time"
)

func TestRunGateSerializes(t *testing.T) {
	g := NewRunGate()
	if !g.TryAcquire() {
		t.Fatal("fresh gate not free")
	}
	if g.TryAcquire() {
		t.Fatal("double acquire succeeded")
	}
	g.Release()
	if !g.TryAcquire() {
		t.Fatal("release did not free the gate")
	}
	g.Release()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := g.Acquire(ctx); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	g.Release()
}
