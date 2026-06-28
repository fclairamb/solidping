package checkhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fclairamb/solidping/server/internal/version"
)

// BenchmarkHTTPCheckerExecute measures allocations on the per-execution HTTP
// check path against a local server. This is the highest-frequency path in the
// system (every check, every period, every region), so allocs/op here are
// multiplied by thousands of executions per minute — small wins compound. It
// also exposes the cost of constructing a fresh http.Client per Execute
// (hypothesis #1): a remediation that reuses the transport should drop allocs/op
// measurably against this baseline.
//
// Run: go test -run=XXX -bench=BenchmarkHTTPCheckerExecute -benchmem ./internal/checkers/checkhttp/
func BenchmarkHTTPCheckerExecute(b *testing.B) {
	if version.UserAgent == "" {
		version.UserAgent = version.DefaultUserAgent()
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	checker := &HTTPChecker{}
	cfg := &HTTPConfig{Method: http.MethodGet, ExpectedStatus: 200, URL: server.URL}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := checker.Execute(ctx, cfg); err != nil {
			b.Fatalf("execute: %v", err)
		}
	}
}

// BenchmarkHTTPCheckerExecuteBodyMatch measures the body-buffering path
// (LimitReader + ReadAll, hypothesis #5): when body matching is on, the response
// body is read fully into memory. Uses a ~64 KiB body so the allocation of the
// read buffer is visible. Compare allocs/op and bytes/op against the no-match
// benchmark above to quantify what body matching costs per execution.
func BenchmarkHTTPCheckerExecuteBodyMatch(b *testing.B) {
	if version.UserAgent == "" {
		version.UserAgent = version.DefaultUserAgent()
	}

	const bodySize = 64 * 1024
	body := strings.Repeat("a", bodySize) + "NEEDLE"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	checker := &HTTPChecker{}
	cfg := &HTTPConfig{Method: http.MethodGet, ExpectedStatus: 200, BodyExpect: "NEEDLE", URL: server.URL}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := checker.Execute(ctx, cfg); err != nil {
			b.Fatalf("execute: %v", err)
		}
	}
}
