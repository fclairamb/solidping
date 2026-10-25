package app

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// sweepSkipped lists routes the route sweep must not drive: they end the
// session, delete the org the sweep runs in, or stream until the client goes
// away.
//
//nolint:gochecknoglobals // Effectively a constant table; Go has no const maps.
var sweepSkipped = map[string]string{
	"DELETE /api/v1/orgs/{org}":     "deletes the org the sweep runs in",
	"GET /api/v1/fake":              "public self-test fixture that fails on purpose",
	"POST /api/v1/auth/logout":      "ends the sweep's own session",
	"POST /api/v1/orgs/{org}/leave": "removes the sweep's own membership",
}

var sweepParam = regexp.MustCompile(`\{[^}]+\}`)

// sweepURL fills every path parameter of pattern: the org slug, the given
// uid for the *Uid / uid / check parameters, and a placeholder for the rest.
func sweepURL(pattern, orgSlug, uid string) string {
	return sweepParam.ReplaceAllStringFunc(pattern, func(param string) string {
		switch name := strings.Trim(param, "{}"); {
		case name == "org":
			return orgSlug
		case name == "components":
			return "x"
		case name == "uid" || name == "check" || strings.HasSuffix(name, "Uid"):
			return uid
		default:
			return "placeholder"
		}
	})
}

// TestEveryRouteAnswersWithoutAServerError walks the REAL route table and
// drives every route as the org owner (with a real check uid, then with a uid
// that exists nowhere, each with an empty and a malformed JSON body) and as an
// anonymous caller. Bad input and missing resources must be refused with a
// 4xx, never answered with a 5xx.
func TestEveryRouteAnswersWithoutAServerError(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	env := newDemoEnv(t)

	check := models.NewCheck(env.org.UID, "sweep-check", "http")
	r.NoError(env.server.dbService.CreateCheck(ctx, check))

	type route struct{ method, pattern string }

	var routes []route

	r.NoError(env.server.router.Walk(func(method, pattern string) error {
		if !strings.HasPrefix(pattern, "/api/v1/") || method == http.MethodOptions {
			return nil
		}

		if _, skip := sweepSkipped[method+" "+pattern]; !skip {
			routes = append(routes, route{method, pattern})
		}

		return nil
	}))
	r.Greater(len(routes), 300, "the API route table looks implausibly small")

	const missingUID = "00000000-0000-4000-8000-000000000000"

	attempts := []struct {
		token, uid, body string
	}{
		{env.plainToken, check.UID, `{}`},
		{env.plainToken, missingUID, `{}`},
		{env.plainToken, missingUID, `{"name":`},
		{"", missingUID, `{}`},
	}

	var serverErrors []string

	for _, rt := range routes {
		for _, at := range attempts {
			status := env.sweep(rt.method, sweepURL(rt.pattern, env.org.Slug, at.uid), at.token, at.body)
			// 503 is how an unconfigured integration (WebAuthn, bots) says so.
			if status >= http.StatusInternalServerError && status != http.StatusServiceUnavailable {
				serverErrors = append(serverErrors, rt.method+" "+rt.pattern)
			}
		}
	}

	r.Empty(serverErrors, "routes answering 5xx to bad input or a missing resource")
}

// sweep issues one request bounded by a short timeout (a streaming route that
// never ends counts as answered) and returns the status, or 0 on timeout.
func (e *demoEnv) sweep(method, path, token, body string) int {
	e.t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, e.ts.URL+path, strings.NewReader(body))
	require.NoError(e.t, err)

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := e.ts.Client().Do(req)
	if err != nil {
		return 0
	}

	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode
}
