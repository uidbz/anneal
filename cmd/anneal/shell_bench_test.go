package main

import (
	"fmt"
	"testing"
)

// BenchmarkStreamDelta measures the cost of one streamed text batch on a
// transcript with the maximum number of visible blocks. Run before/after the
// refresh-scoping work:
//
//	go test ./cmd/anneal -run '^$' -bench StreamDelta -benchmem
func BenchmarkStreamDelta(b *testing.B) {
	sh := newTestShell(b)
	for i := 0; i < 300; i++ {
		sh.appendBlock(sh.noticeBlock(noticeInfo,
			"a moderately long notice line that wraps at the transcript width"))
	}
	ev := normalizeEvent(map[string]any{"type": "text.delta", "run": 1, "text": "some streamed words "})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sh.handleEvent(ev)
		// Keep the stream bounded: freeze-and-drop the chunks periodically
		// without adding final transcript blocks.
		if i%200 == 199 {
			sh.closeTail("")
		}
	}
}

// BenchmarkSessionReplay measures opening a large stored session.
//
//	go test ./cmd/anneal -run '^$' -bench SessionReplay -benchmem
func BenchmarkSessionReplay(b *testing.B) {
	sh := newTestShell(b)
	blocks := make([]any, 0, 600)
	for i := 0; i < 600; i++ {
		blocks = append(blocks, map[string]any{
			"kind": "assistant", "text": fmt.Sprintf("answer %d with some prose", i),
		})
	}
	ev := normalizeEvent(map[string]any{"type": "session.opened", "id": "s", "blocks": blocks})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sh.resetChat()
		sh.handleEvent(ev)
	}
}
