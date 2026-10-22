package db

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestConnectGivesUpWhenTheContextEnds(t *testing.T) {
	t.Parallel()
	// A port nothing listens on: every ping fails.
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := Connect(ctx, "postgres://nobody@"+addr+"/x?connect_timeout=1"); err == nil {
		t.Fatal("Connect() succeeded against a closed port")
	}
	// It retried for the context's 3 seconds, not the full minute.
	if elapsed := time.Since(start); elapsed < 2*time.Second || elapsed > 10*time.Second {
		t.Errorf("gave up after %v, want about 3s", elapsed)
	}
}

func TestConnectRejectsABadURL(t *testing.T) {
	t.Parallel()
	if _, err := Connect(t.Context(), "postgres://%zz"); err == nil {
		t.Error("Connect() accepted a malformed URL")
	}
}
