package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestServeShutsDownWhenContextIsCancelled(t *testing.T) {
	addr := freeAddr(t)
	logger := slog.New(slog.DiscardHandler)
	srv := NewServer(addr, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), logger)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, logger, srv) }()

	waitUntilServing(t, "http://"+addr)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve() = %v, want nil after cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve() did not return after cancel")
	}
}

func TestServeFailsFastWhenAPortIsTaken(t *testing.T) {
	taken, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = taken.Close() }()

	logger := slog.New(slog.DiscardHandler)
	free := freeAddr(t)
	err = Serve(t.Context(), logger,
		NewServer(free, http.NotFoundHandler(), logger),
		NewServer(taken.Addr().String(), http.NotFoundHandler(), logger),
	)
	if err == nil || !strings.Contains(err.Error(), "listen on "+taken.Addr().String()) {
		t.Fatalf("Serve() = %v, want a listen error for %s", err, taken.Addr())
	}

	// The first server's port must have been released, not leaked.
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", free)
	if err != nil {
		t.Fatalf("port %s still held after failed startup: %v", free, err)
	}
	_ = ln.Close()
}

// freeAddr returns a localhost address that was free a moment ago.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

func waitUntilServing(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never started serving", url)
}
