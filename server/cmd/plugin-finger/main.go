// Command plugin-finger serves the reference HTTP check plugin `x-finger`
// (spec 2026-10-17-06). Run it next to the worker that calls it:
//
//	SP_PLUGIN_FINGER_KEYS='[{"id":"2026-10","secret":"…"}]' plugin-finger -listen 127.0.0.1:7979
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fclairamb/solidping/server/internal/plugins/examples/finger"
	"github.com/fclairamb/solidping/server/internal/servicesig"
)

// keysEnv holds the signing key set, the same one the plugin manifest names.
const keysEnv = "SP_PLUGIN_FINGER_KEYS"

var errNoKeys = errors.New(keysEnv + " must hold a signing key set")

func main() {
	listen := flag.String("listen", "127.0.0.1:7979", "address to listen on")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := run(*listen, logger); err != nil {
		logger.Error("Finger plugin stopped", "error", err)
		os.Exit(1)
	}
}

func run(listen string, logger *slog.Logger) error {
	keys, err := servicesig.ParseKeySet(os.Getenv(keysEnv))
	if err != nil {
		return fmt.Errorf("%w: %w", errNoKeys, err)
	}

	if keys.Empty() {
		return errNoKeys
	}

	server := &http.Server{
		Addr:              listen,
		Handler:           finger.NewHandler(keys, logger),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		// A run takes at most 30 s.
		WriteTimeout: 40 * time.Second,
		IdleTimeout:  2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()

		_ = server.Shutdown(shutdownCtx)
	}()

	logger.InfoContext(ctx, "Finger plugin listening", "listen", listen)

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serving: %w", err)
	}

	return nil
}
