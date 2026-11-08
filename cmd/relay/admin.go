package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

const (
	// envAdminAddr names the probe listener; defaultAdminAddr is its default.
	envAdminAddr     = "ADMIN_ADDR"
	defaultAdminAddr = ":8094"
	// adminShutdownTimeout bounds the probe server's drain.
	adminShutdownTimeout = 5 * time.Second
)

// admin is the relay's tiny probe server: GET /healthz answers 200 while the
// process runs, so a container smoke test or a kubelet httpGet probe has
// something to ask. It carries no readiness gate: the relay's work is the
// outbox loop, and a transient broker or database failure is retried by the
// loop itself, never by a restart.
type admin struct {
	srv *http.Server
	ln  net.Listener
}

// startAdmin binds addr and serves /healthz in the background. The returned
// address is the bound one (a test passes ":0").
func startAdmin(addr string, logger *slog.Logger) (*admin, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s (%s): %w", addr, envAdminAddr, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	a := &admin{srv: &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}, ln: ln}
	go func() {
		if err := a.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("relay admin server stopped", "error", err)
		}
	}()
	return a, nil
}

// addr is the bound listen address.
func (a *admin) addr() string { return a.ln.Addr().String() }

// close drains the probe server within adminShutdownTimeout.
func (a *admin) close() error {
	ctx, cancel := context.WithTimeout(context.Background(), adminShutdownTimeout)
	defer cancel()
	return a.srv.Shutdown(ctx)
}
