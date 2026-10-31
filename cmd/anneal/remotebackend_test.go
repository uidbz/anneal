package main

import (
	"context"
	"testing"
	"time"

	coreapp "github.com/uidbz/anneal/core/app"
	"github.com/uidbz/anneal/core/server"
)

// TestRemoteBackendRoundTrip boots a real anneald (core app + protocol
// server) on a loopback port and drives it through remoteBackend: the RPC
// plumbing, cache seeding, and typed query decoders must agree with the
// server's payloads.
func TestRemoteBackendRoundTrip(t *testing.T) {
	t.Setenv("ANNEAL_HOME", t.TempDir())
	a, err := coreapp.New()
	if err != nil {
		t.Fatal(err)
	}
	a.Cfg.Server.Listen = []string{"127.0.0.1:0"}
	a.Cfg.Server.AuthTokenEnv = ""
	s, err := server.New(a, "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = s.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	var addr string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && addr == "" {
		if addrs := s.Addrs(); len(addrs) > 0 {
			addr = addrs[0]
		} else {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if addr == "" {
		t.Fatal("server never bound")
	}

	rb, err := dialRemote(ctx, "test", addr, "")
	if err != nil {
		t.Fatalf("dialRemote: %v", err)
	}
	if !rb.remote() {
		t.Fatal("remote() false for a dialed backend")
	}
	if rb.describe() != "test" {
		t.Fatalf("describe: %q", rb.describe())
	}
	if rb.mode() == "" {
		t.Fatal("mode cache not seeded from server.info")
	}
	doc, err := rb.configDoc()
	if err != nil || doc == "" {
		t.Fatalf("configDoc: %v len=%d", err, len(doc))
	}
	if mv, err := rb.memoryView(); err != nil || mv == nil {
		t.Fatalf("memoryView: %v", err)
	}
	if l, err := rb.skillsList(); err != nil || l == nil {
		t.Fatalf("skillsList: %v", err)
	}
	if l, err := rb.journeyTail(5); err != nil || l == nil {
		t.Fatalf("journeyTail: %v", err)
	}
	if l, err := rb.improvementRuns(5); err != nil || l == nil {
		t.Fatalf("improvementRuns: %v", err)
	}
	if rb.harnessOverlay() {
		t.Fatal("overlay true on a fresh backend")
	}
	if rb.paneAPI()["harnessInfo"] == nil {
		t.Fatal("paneAPI missing harnessInfo")
	}
	// sessions() seeds asynchronously; poll briefly.
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(rb.sessions()) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(rb.sessions()) == 0 {
		t.Fatal("sessions list never populated")
	}
}
