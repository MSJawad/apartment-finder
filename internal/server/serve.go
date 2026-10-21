package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// shutdownTimeout bounds how long in-flight requests get to finish once we
// stop. It stays under Kubernetes' default 30 second grace period, so we exit
// on our own before the kubelet sends SIGKILL.
const shutdownTimeout = 10 * time.Second

// NewServer returns an http.Server with timeouts, so a slow or stalled client
// can't hold a connection open forever, and with the server's own error
// messages routed through logger as JSON.
func NewServer(addr string, h http.Handler, logger *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
}

// Serve binds every server's address, serves until ctx is cancelled or one of
// them fails, then shuts them all down gracefully.
//
// Binding up front makes a taken port fail startup immediately. If a server
// fails later, Serve returns its error so the process exits and Kubernetes
// restarts it, instead of leaving it half working.
func Serve(ctx context.Context, logger *slog.Logger, servers ...*http.Server) error {
	listeners := make([]net.Listener, 0, len(servers))
	for _, srv := range servers {
		var lc net.ListenConfig
		ln, err := lc.Listen(ctx, "tcp", srv.Addr)
		if err != nil {
			for _, open := range listeners {
				_ = open.Close() // already failing; the bind error is the one to report
			}
			return fmt.Errorf("listen on %s: %w", srv.Addr, err)
		}
		listeners = append(listeners, ln)
	}

	errs := make(chan error, len(servers))
	for i, srv := range servers {
		ln := listeners[i]
		logger.LogAttrs(ctx, slog.LevelInfo, "listening", slog.String("addr", ln.Addr().String()))
		go func() {
			if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				errs <- fmt.Errorf("server on %s: %w", ln.Addr(), err)
			}
		}()
	}

	var serveErr error
	select {
	case <-ctx.Done():
		logger.LogAttrs(ctx, slog.LevelInfo, "shutting down")
	case serveErr = <-errs:
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	allErrs := []error{serveErr}
	for _, srv := range servers {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			allErrs = append(allErrs, fmt.Errorf("shut down server on %s: %w", srv.Addr, err))
		}
	}
	return errors.Join(allErrs...)
}
