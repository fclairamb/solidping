package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

// faultHook fails the Nth query seen after arm() by handing bun an already
// canceled context, so each query along a request path fails in turn without
// closing the pool.
type faultHook struct {
	seen   atomic.Int64
	failAt atomic.Int64 // 0: count only
	armed  atomic.Bool
}

func (h *faultHook) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	if !h.armed.Load() {
		return ctx
	}

	n := h.seen.Add(1)
	if f := h.failAt.Load(); f != 0 && n == f {
		canceled, cancel := context.WithCancel(ctx)
		cancel()

		return canceled
	}

	return ctx
}

func (h *faultHook) AfterQuery(context.Context, *bun.QueryEvent) {}

// faultBodies are the request bodies each route is driven with: an empty one
// and a permissive one carrying the fields most create/update routes accept.
//
//nolint:gochecknoglobals // Effectively a constant table.
var faultBodies = []string{
	`{}`,
	`{"name":"renamed","slug":"renamed-slug","title":"renamed","description":"d","type":"http",` +
		`"url":"https://acme.com","email":"carol@acme.com","role":"user","enabled":true,"period":"00:01:00",` +
		`"config":{"url":"https://acme.com"},"settings":{"url":"https://acme.com/hook"},"level":2,` +
		`"startAt":"2030-01-01T00:00:00Z","endAt":"2030-01-02T00:00:00Z","message":"m","status":"investigating"}`,
}

// seedFaultFixtures creates one resource per collection through the real API
// and returns collection -> uid.
func seedFaultFixtures(t *testing.T, env *demoEnv) map[string]string {
	t.Helper()

	seeds := []struct{ path, body string }{
		{"checks", `{"type":"http","name":"c1","config":{"url":"https://acme.com"}}`},
		{"check-groups", `{"name":"G1","slug":"grp-one"}`},
		{"status-pages", `{"name":"SP","slug":"sp1"}`},
		{"severities", `{"name":"sev","slug":"sev-one","level":1}`},
		{"maintenance-windows", `{"title":"mw","startAt":"2030-01-01T00:00:00Z","endAt":"2030-01-02T00:00:00Z"}`},
		{"escalation-policies", `{"name":"ep"}`},
		{"on-call-schedules", `{"name":"oc","handoffTime":"09:00"}`},
		{"integrations", `{"type":"webhook","name":"w","settings":{"url":"https://acme.com/hook"}}`},
		{"channels", `{"type":"webhook","name":"w","settings":{"url":"https://acme.com/hook"}}`},
		{"report-schedules", `{"name":"rs"}`},
		{"invitations", `{"email":"bob@acme.com","role":"user"}`},
	}

	uids := map[string]string{}

	for _, s := range seeds {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
			env.ts.URL+"/api/v1/orgs/"+env.org.Slug+"/"+s.path, strings.NewReader(s.body))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+env.plainToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := env.ts.Client().Do(req)
		require.NoError(t, err)

		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		var doc struct {
			UID string `json:"uid"`
		}
		if json.Unmarshal(raw, &doc) == nil && doc.UID != "" {
			uids[s.path] = doc.UID
		}
	}

	return uids
}

// faultURL fills path parameters with the seeded uid of the collection the
// parameter follows, falling back to the check uid.
func faultURL(pattern, orgSlug string, uids map[string]string) string {
	segs := strings.Split(pattern, "/")
	for i, seg := range segs {
		if !strings.HasPrefix(seg, "{") {
			continue
		}

		name := strings.Trim(seg, "{}")

		switch {
		case name == "org":
			segs[i] = orgSlug
		case name == "components":
			segs[i] = "x"
		case name == "uid" || name == "check" || strings.HasSuffix(name, "Uid"):
			uid := uids["checks"]
			if i > 0 {
				if u, ok := uids[segs[i-1]]; ok {
					uid = u
				}
			}

			segs[i] = uid
		default:
			segs[i] = "placeholder"
		}
	}

	return strings.Join(segs, "/")
}

// TestEveryRouteSurvivesAFailingQuery drives every route against seeded data
// and, for each, fails its 1st..Nth database query in turn. A failing store
// must be answered with a response (never a hang, never a dropped connection).
func TestEveryRouteSurvivesAFailingQuery(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newDemoEnv(t)
	hook := &faultHook{}
	env.server.dbService.DB().AddQueryHook(hook)

	uids := seedFaultFixtures(t, env)
	r.NotEmpty(uids["checks"])

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

	const maxFaults = 10

	var hung []string

	deadline := time.Now().Add(8 * time.Minute)

	for _, rt := range routes {
		if time.Now().After(deadline) {
			break
		}

		url := faultURL(rt.pattern, env.org.Slug, uids)

		for _, body := range faultBodies {
			hook.seen.Store(0)
			hook.failAt.Store(0)
			hook.armed.Store(true)
			env.sweep(rt.method, url, env.plainToken, body)

			queries := int(hook.seen.Load())
			if queries > maxFaults {
				queries = maxFaults
			}

			for n := 1; n <= queries; n++ {
				hook.seen.Store(0)
				hook.failAt.Store(int64(n))

				if env.sweep(rt.method, url, env.plainToken, body) == 0 {
					hung = append(hung, rt.method+" "+rt.pattern)
				}
			}

			hook.armed.Store(false)
		}
	}

	r.Empty(hung, "routes that never answered when a query failed")
}
