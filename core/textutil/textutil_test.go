package textutil

import "testing"

func TestClip(t *testing.T) {
	if got := Clip("short", 10); got != "short" {
		t.Fatalf("no-op clip: %q", got)
	}
	if got := Clip("abcdef", 3); got != "abc…" {
		t.Fatalf("clip: %q", got)
	}
	if got := Clip("abcdef", 0); got != "abcdef" {
		t.Fatalf("zero should mean no cap: %q", got)
	}
}
