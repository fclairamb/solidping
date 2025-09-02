// Package profiler provides an optional pprof profiler server.
package profiler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
)

const readHeaderTimeout = 10 * time.Second

// Server is an optional pprof profiler server.
type Server struct {
	config *config.ProfilerConfig
	srv    *http.Server
}

// New creates a new profiler server.
func New(cfg *config.ProfilerConfig) *Server {
	return &Server{config: cfg}
}

// Start starts the profiler server if enabled.
// Returns immediately if profiler is disabled.
func (s *Server) Start(ctx context.Context) error {
	if !s.config.Enabled {
		slog.InfoContext(ctx, "Profiler server disabled")
		return nil
	}

	mux := http.NewServeMux()

	// Register pprof handlers
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	// Health check for the profiler server
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	s.srv = &http.Server{
		Addr:              s.config.Listen,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	slog.InfoContext(ctx, "Starting profiler server", "listen", s.config.Listen)

	go func() {
		if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.ErrorContext(ctx, "Profiler server error", "error", err)
		}
	}()

	return nil
}

// Shutdown gracefully shuts down the profiler server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}
