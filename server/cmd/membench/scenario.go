package main

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// scenario is one named, parameterised workload. Everything a scenario needs is
// declared here — the server env it boots with, what it does during warm-up,
// and what (if anything) keeps running during the sample window — so the set of
// things being measured is a table a reader can audit, not a pile of flags.
type scenario struct {
	Name string
	// Env is merged over the base env when the server starts.
	Env map[string]string
	// Prepare runs once, before the warm-up, on a healthy server.
	Prepare func(ctx context.Context, c *client) error
	// Load runs repeatedly (with LoadEvery between iterations) from the start
	// of warm-up until the end of the sample window. Nil means idle.
	Load      func(ctx context.Context, c *client) error
	LoadEvery time.Duration
	// Caveat is appended to the result — typically what the scenario cannot
	// measure. A caveat that does not reach the report does not exist.
	Caveat string
	// LocalOnly marks a scenario that cannot run in a container (embedded
	// Postgres downloads a distribution at boot).
	LocalOnly bool
}

// sqliteEnv is the in-container-friendly database configuration: file-backed
// SQLite in a scratch directory, reset on every boot.
func sqliteEnv(role string) map[string]string {
	env := map[string]string{
		"SP_DB_TYPE":   "sqlite",
		"SP_NODE_ROLE": role,
	}
	if role != "all" && role != "api" {
		// A checks role must declare a region or the node refuses to start.
		env["SP_NODE_REGION"] = "bench"
	}

	return env
}

// checksScenario builds a `checks N=…` scenario: N HTTP checks pointed at the
// server's own health endpoint, executed by the node's own checks role for the
// whole window.
func checksScenario(count int) scenario {
	return scenario{
		Name: "checks-" + strconv.Itoa(count),
		Env:  sqliteEnv("all"),
		Prepare: func(ctx context.Context, c *client) error {
			return c.bulkCreateChecks(ctx, count, "10s", c.baseURL+"/api/mgmt/health")
		},
		Caveat: fmt.Sprintf("%d HTTP checks at a 10s period against the node's own /api/mgmt/health", count),
	}
}

// allScenarios is the fixed scenario set. Fixed on purpose: a bench whose
// scenario list drifts between runs cannot be diffed.
func allScenarios() []scenario {
	return []scenario{
		{
			Name:   "idle-all-sqlite",
			Env:    sqliteEnv("all"),
			Caveat: "the reference idle shape: every role in one process, no traffic",
		},
		{
			Name:   "idle-api-sqlite",
			Env:    sqliteEnv("api"),
			Caveat: "API role only — no scheduler, no check runners",
		},
		{
			Name: "idle-api-checks-sqlite",
			Env:  sqliteEnv("api,checks"),
			Caveat: "API + checks, no jobs scheduler. A checks-ONLY node serves no HTTP at all " +
				"(server.go serveAPIOrWait), so it cannot be sampled from inside; this is the closest " +
				"in-process approximation, and the delta against idle-all isolates the jobs role, not the API surface",
		},
		{
			Name:      "idle-api-postgres",
			Env:       map[string]string{"SP_DB_TYPE": "postgres-embedded", "SP_NODE_ROLE": "api"},
			LocalOnly: true,
			Caveat:    "embedded Postgres downloads and runs its own distribution, so this scenario is local-mode only",
		},
		checksScenario(200),
		checksScenario(500),
		checksScenario(1000),
		{
			Name: "login-burst",
			Env:  sqliteEnv("all"),
			Load: loginBurst,
			// Deliberately frequent: argon2id is a 64 MiB-per-hash transient by
			// design (a security parameter, not a bug), and the point is to see
			// how high the bounded concurrency lets the peak go.
			LoadEvery: 2 * time.Second,
			Caveat:    "argon2id peak — the 64 MiB per hash is a SECURITY parameter and is measured, never lowered",
		},
		{
			Name:      "docs-crawl",
			Env:       sqliteEnv("all"),
			Load:      docsCrawl,
			LoadEvery: 10 * time.Second,
			Caveat: "every path in /docs/sitemap.xml, once per iteration — the embedded-asset peak, " +
				"which is a peak and not a baseline",
		},
		{
			Name:      "dash0-reload",
			Env:       sqliteEnv("all"),
			Load:      dash0Reload,
			LoadEvery: 5 * time.Second,
			Caveat:    "50 dashboard shell loads per iteration",
		},
	}
}

// loginBurst fires concurrent logins. Each one is a deliberate 64 MiB argon2id
// derivation, bounded in-process to min(GOMAXPROCS,4) concurrent hashes.
func loginBurst(ctx context.Context, c *client) error {
	const burst = 12

	var wg sync.WaitGroup

	wg.Add(burst)

	for range burst {
		go func() {
			defer wg.Done()

			// A fresh client per goroutine: sharing the token would skip the
			// hash, which is the entire cost being measured.
			burstClient := newClient(c.baseURL)
			_ = burstClient.login(ctx)
		}()
	}

	wg.Wait()

	return nil
}

// docsCrawl fetches every documented path once. Cached between iterations so
// the sitemap is not re-fetched each time.
func docsCrawl(ctx context.Context, c *client) error {
	paths, err := cachedDocsPaths(ctx, c)
	if err != nil {
		return err
	}

	for _, path := range paths {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		_ = c.get(ctx, path)
	}

	return nil
}

// docsPathCache memoises the sitemap per base URL.
//
//nolint:gochecknoglobals // process-lifetime memo for a single-run CLI
var (
	docsPathCache   = map[string][]string{}
	docsPathCacheMu sync.Mutex
)

func cachedDocsPaths(ctx context.Context, c *client) ([]string, error) {
	docsPathCacheMu.Lock()
	defer docsPathCacheMu.Unlock()

	if paths, ok := docsPathCache[c.baseURL]; ok {
		return paths, nil
	}

	paths, err := c.docsPaths(ctx)
	if err != nil {
		return nil, err
	}

	docsPathCache[c.baseURL] = paths

	return paths, nil
}

// dash0Reload loads the dashboard shell repeatedly, the way a browser refresh
// does.
func dash0Reload(ctx context.Context, c *client) error {
	const reloads = 50

	for range reloads {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		_ = c.get(ctx, "/dash0/")
	}

	return nil
}
