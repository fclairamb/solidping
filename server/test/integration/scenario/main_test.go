package scenario

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"testing"

	"github.com/fclairamb/solidping/server/internal/db/postgres/embeddedpg"
)

var errUnexpectedAddrType = errors.New("unexpected listener address type")

// TestMain boots one embedded-Postgres instance shared across all scenario
// tests, then runs the test suite. Using a shared instance avoids the ~2s
// per-test startup penalty while still giving full Postgres semantics
// (LISTEN/NOTIFY, FOR UPDATE SKIP LOCKED, etc.).
//
// Each test creates its own org (random slug) for isolation, so tests are safe
// to run in parallel.
func TestMain(m *testing.M) {
	const (
		scenarioPGUser   = "postgres"
		scenarioPGPass   = "postgres"
		scenarioPGDBName = "solidping_scenario"
	)

	// Pick a random free port to avoid conflicts when re-running tests quickly.
	port, err := freePort()
	if err != nil {
		fmt.Fprintf(os.Stderr, "scenario: failed to find free port: %v\n", err)
		os.Exit(1)
	}

	inst, startErr := embeddedpg.Start(embeddedpg.Options{
		Suite:    "scenario",
		Port:     uint32(port),
		Database: scenarioPGDBName,
		Username: scenarioPGUser,
		Password: scenarioPGPass,
		StartParameters: map[string]string{
			"dynamic_shared_memory_type": "posix",
			"shared_buffers":             "256kB",
			"max_connections":            "50",
		},
	})
	if startErr != nil {
		// Embedded postgres not available (e.g. no internet access to download
		// binaries, or unsupported arch). Skip gracefully — tests will call
		// t.Skip via NewPostgresScenario when sharedDB.dbURL is empty.
		fmt.Fprintf(os.Stderr, "scenario: embedded postgres start failed (tests will be skipped): %v\n", startErr)
		os.Exit(m.Run()) // runs; each test will skip via NewPostgresScenario
	}

	defer func() {
		if stopErr := inst.Stop(); stopErr != nil {
			fmt.Fprintf(os.Stderr, "scenario: embedded postgres stop failed: %v\n", stopErr)
		}
	}()

	sharedDB.dbURL = inst.DSN()

	// Run migrations once before tests start to avoid races.
	if migrateErr := runMigrationsOnce(sharedDB.dbURL); migrateErr != nil {
		fmt.Fprintf(os.Stderr, "scenario: migrations failed: %v\n", migrateErr)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// freePort finds an available TCP port on localhost.
func freePort() (int, error) {
	// Try random ports in a sensible range to avoid well-known conflicts.
	base := 5450 + rand.Intn(500)

	lc := &net.ListenConfig{}

	for i := range 20 {
		port := base + i
		ln, listenErr := lc.Listen(context.Background(), "tcp", fmt.Sprintf("localhost:%d", port))
		if listenErr == nil {
			_ = ln.Close()
			return port, nil
		}
	}

	// Fall back to OS-assigned port.
	ln, err := lc.Listen(context.Background(), "tcp", "localhost:0")
	if err != nil {
		return 0, err
	}

	defer func() { _ = ln.Close() }()

	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, errUnexpectedAddrType
	}

	return addr.Port, nil
}
