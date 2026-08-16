package checkprometheus_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkprometheus"
)

// sampleExposition is a small but representative /metrics body: a gauge with
// several label sets, a counter, an untyped metric, a histogram and a summary.
const sampleExposition = `# HELP process_open_fds Number of open file descriptors.
# TYPE process_open_fds gauge
process_open_fds 42
# HELP queue_depth Depth per queue.
# TYPE queue_depth gauge
queue_depth{queue="a",dc="eu"} 10
queue_depth{queue="b",dc="eu"} 30
queue_depth{queue="c",dc="us"} 20
# HELP http_requests_total Total requests.
# TYPE http_requests_total counter
http_requests_total{code="200"} 1234
# HELP build_info Untyped build marker.
# TYPE build_info untyped
build_info 1
# HELP rq_seconds Request duration.
# TYPE rq_seconds histogram
rq_seconds_bucket{le="0.5"} 3
rq_seconds_bucket{le="1"} 7
rq_seconds_bucket{le="+Inf"} 9
rq_seconds_sum 4.5
rq_seconds_count 9
# HELP gc_seconds GC pauses.
# TYPE gc_seconds summary
gc_seconds{quantile="0.5"} 0.01
gc_seconds{quantile="0.99"} 0.2
gc_seconds_sum 1.5
gc_seconds_count 100
`

// metricsServer serves a fixed exposition body and records the last request.
func metricsServer(t *testing.T, body string) (*httptest.Server, func() http.Header) {
	t.Helper()

	var (
		mu     sync.Mutex
		header http.Header
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		header = r.Header.Clone()
		mu.Unlock()

		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	return server, func() http.Header {
		mu.Lock()
		defer mu.Unlock()

		return header
	}
}

// run validates and executes a config against the checker.
func run(ctx context.Context, t *testing.T, cfg *checkprometheus.PrometheusConfig) *checkerdef.Result {
	t.Helper()

	r := require.New(t)
	checker := &checkprometheus.PrometheusChecker{}

	spec := &checkerdef.CheckSpec{Config: cfg.GetConfig()}
	r.NoError(checker.Validate(spec), "config must validate")

	parsed := &checkprometheus.PrometheusConfig{}
	r.NoError(parsed.FromMap(spec.Config))

	result, err := checker.Execute(ctx, parsed)
	r.NoError(err)
	r.NotNil(result)

	return result
}

func TestPrometheusChecker_Type(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal(checkerdef.CheckTypePrometheus, (&checkprometheus.PrometheusChecker{}).Type())
}

// TestScrape_Grading walks the whole threshold matrix for both directions.
func TestScrape_Grading(t *testing.T) {
	t.Parallel()

	server, _ := metricsServer(t, sampleExposition)

	tests := []struct {
		name       string
		metric     string
		operator   string
		warning    *float64
		critical   *float64
		wantStatus checkerdef.Status
		wantValue  float64
	}{
		{
			name: "greater within thresholds", metric: "process_open_fds", operator: ">",
			warning: f64(800), critical: f64(1000),
			wantStatus: checkerdef.StatusUp, wantValue: 42,
		},
		{
			name: "greater breaches warning only", metric: "process_open_fds", operator: ">",
			warning: f64(10), critical: f64(1000),
			wantStatus: checkerdef.StatusWarning, wantValue: 42,
		},
		{
			name: "greater breaches critical", metric: "process_open_fds", operator: ">",
			warning: f64(10), critical: f64(20),
			wantStatus: checkerdef.StatusDown, wantValue: 42,
		},
		{
			name: "greater-equal boundary breaches", metric: "process_open_fds", operator: ">=",
			critical:   f64(42),
			wantStatus: checkerdef.StatusDown, wantValue: 42,
		},
		{
			name: "less within thresholds (free disk shape)", metric: "process_open_fds", operator: "<",
			warning: f64(20), critical: f64(5),
			wantStatus: checkerdef.StatusUp, wantValue: 42,
		},
		{
			name: "less breaches warning only", metric: "process_open_fds", operator: "<",
			warning: f64(100), critical: f64(5),
			wantStatus: checkerdef.StatusWarning, wantValue: 42,
		},
		{
			name: "less breaches critical", metric: "process_open_fds", operator: "<",
			warning: f64(100), critical: f64(50),
			wantStatus: checkerdef.StatusDown, wantValue: 42,
		},
		{
			name: "less-equal boundary breaches", metric: "process_open_fds", operator: "<=",
			critical:   f64(42),
			wantStatus: checkerdef.StatusDown, wantValue: 42,
		},
		{
			name: "equal matches", metric: "build_info", operator: "==",
			critical:   f64(1),
			wantStatus: checkerdef.StatusDown, wantValue: 1,
		},
		{
			name: "equal does not match", metric: "build_info", operator: "==",
			critical:   f64(0),
			wantStatus: checkerdef.StatusUp, wantValue: 1,
		},
		{
			name: "not-equal matches", metric: "build_info", operator: "!=",
			critical:   f64(0),
			wantStatus: checkerdef.StatusDown, wantValue: 1,
		},
		{
			name: "not-equal does not match", metric: "build_info", operator: "!=",
			critical:   f64(1),
			wantStatus: checkerdef.StatusUp, wantValue: 1,
		},
		{
			name: "counter reads directly", metric: "http_requests_total", operator: ">",
			critical:   f64(1000),
			wantStatus: checkerdef.StatusDown, wantValue: 1234,
		},
		{
			name: "warning-only config never pages", metric: "process_open_fds", operator: ">",
			warning:    f64(10),
			wantStatus: checkerdef.StatusWarning, wantValue: 42,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			result := run(t.Context(), t, &checkprometheus.PrometheusConfig{
				URL: server.URL, Metric: tt.metric, Operator: tt.operator,
				WarningValue: tt.warning, CriticalValue: tt.critical,
			})

			r.Equal(tt.wantStatus, result.Status, "output: %v", result.Output)
			r.InDelta(tt.wantValue, result.Metrics["value"], 1e-9)
			r.Equal("scrape", result.Output["mode"])
		})
	}
}

// TestScrape_ZeroWarningIsHonored proves the pointer-presence contract end to
// end: a `warningValue: 0` grades as a real threshold rather than being read as
// "unset". The positive control is the second case, where an actual unset
// warning leaves the same value Up.
func TestScrape_ZeroWarningIsHonored(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server, _ := metricsServer(t, "# TYPE free_slots gauge\nfree_slots 0\n")

	withZero := run(t.Context(), t, &checkprometheus.PrometheusConfig{
		URL: server.URL, Metric: "free_slots", Operator: "<=", WarningValue: f64(0),
	})
	r.Equal(checkerdef.StatusWarning, withZero.Status, "warningValue: 0 must be a live threshold")

	withoutWarning := run(t.Context(), t, &checkprometheus.PrometheusConfig{
		URL: server.URL, Metric: "free_slots", Operator: "<=", CriticalValue: f64(-1),
	})
	r.Equal(checkerdef.StatusUp, withoutWarning.Status, "positive control: no warning tier, no warning")
}

func TestScrape_LabelSubsetMatching(t *testing.T) {
	t.Parallel()

	server, _ := metricsServer(t, sampleExposition)

	tests := []struct {
		name      string
		labels    map[string]string
		wantValue float64
		wantErr   string
	}{
		{name: "exact single label", labels: map[string]string{"queue": "a"}, wantValue: 10},
		{
			name:   "subset over several labels",
			labels: map[string]string{"queue": "b", "dc": "eu"}, wantValue: 30,
		},
		{
			name:   "partial label still ambiguous",
			labels: map[string]string{"dc": "eu"}, wantErr: "multiple series",
		},
		{
			name:   "value mismatch selects nothing",
			labels: map[string]string{"queue": "zzz"}, wantErr: "no series matched",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			result := run(t.Context(), t, &checkprometheus.PrometheusConfig{
				URL: server.URL, Metric: "queue_depth", Labels: tt.labels,
				Operator: ">", CriticalValue: f64(1e9),
			})

			if tt.wantErr != "" {
				r.Equal(checkerdef.StatusDown, result.Status)
				r.Contains(fmt.Sprint(result.Output[checkerdef.OutputKeyError]), tt.wantErr)

				return
			}

			r.Equal(checkerdef.StatusUp, result.Status)
			r.InDelta(tt.wantValue, result.Metrics["value"], 1e-9)
		})
	}
}

// TestScrape_MultiSeriesMatch covers `single` (an error, never a silent pick)
// and each aggregation. queue_depth has three series: 10, 30, 20.
func TestScrape_MultiSeriesMatch(t *testing.T) {
	t.Parallel()

	server, _ := metricsServer(t, sampleExposition)

	tests := []struct {
		name      string
		match     string
		wantValue float64
		wantErr   bool
	}{
		{name: "single errors on ambiguity", match: "single", wantErr: true},
		{name: "min", match: "min", wantValue: 10},
		{name: "max", match: "max", wantValue: 30},
		{name: "sum", match: "sum", wantValue: 60},
		{name: "avg", match: "avg", wantValue: 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			result := run(t.Context(), t, &checkprometheus.PrometheusConfig{
				URL: server.URL, Metric: "queue_depth", Match: tt.match,
				Operator: ">", CriticalValue: f64(1e9),
			})

			if tt.wantErr {
				r.Equal(checkerdef.StatusDown, result.Status)
				r.Contains(fmt.Sprint(result.Output[checkerdef.OutputKeyError]), "multiple series")
				r.Nil(result.Metrics, "an ambiguous selector must not report a value")

				return
			}

			r.Equal(checkerdef.StatusUp, result.Status)
			r.InDelta(tt.wantValue, result.Metrics["value"], 1e-9)
			r.Equal(3, result.Output["matched_count"])
		})
	}
}

// TestScrape_HistogramAndSummaryFlattening proves the flattened series names
// need no special casing in the selector.
func TestScrape_HistogramAndSummaryFlattening(t *testing.T) {
	t.Parallel()

	server, _ := metricsServer(t, sampleExposition)

	tests := []struct {
		name      string
		metric    string
		labels    map[string]string
		wantValue float64
	}{
		{name: "histogram sum", metric: "rq_seconds_sum", wantValue: 4.5},
		{name: "histogram count", metric: "rq_seconds_count", wantValue: 9},
		{
			name: "histogram bucket by le", metric: "rq_seconds_bucket",
			labels: map[string]string{"le": "0.5"}, wantValue: 3,
		},
		{
			name: "histogram +Inf bucket", metric: "rq_seconds_bucket",
			labels: map[string]string{"le": "+Inf"}, wantValue: 9,
		},
		{name: "summary sum", metric: "gc_seconds_sum", wantValue: 1.5},
		{name: "summary count", metric: "gc_seconds_count", wantValue: 100},
		{
			name: "summary quantile", metric: "gc_seconds",
			labels: map[string]string{"quantile": "0.99"}, wantValue: 0.2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			result := run(t.Context(), t, &checkprometheus.PrometheusConfig{
				URL: server.URL, Metric: tt.metric, Labels: tt.labels,
				Operator: ">", CriticalValue: f64(1e9),
			})

			r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)
			r.InDelta(tt.wantValue, result.Metrics["value"], 1e-9)
		})
	}
}

func TestScrape_OnMissing(t *testing.T) {
	t.Parallel()

	server, _ := metricsServer(t, sampleExposition)

	tests := []struct {
		name      string
		onMissing string
		want      checkerdef.Status
	}{
		{name: "default is down", onMissing: "", want: checkerdef.StatusDown},
		{name: "down", onMissing: "down", want: checkerdef.StatusDown},
		{name: "warning", onMissing: "warning", want: checkerdef.StatusWarning},
		{name: "up", onMissing: "up", want: checkerdef.StatusUp},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			result := run(t.Context(), t, &checkprometheus.PrometheusConfig{
				URL: server.URL, Metric: "nope_not_here", OnMissing: tt.onMissing,
				Operator: ">", CriticalValue: f64(1),
			})

			r.Equal(tt.want, result.Status)
			r.Equal(0, result.Output["matched_count"])
			r.Nil(result.Metrics, "a missing metric has no value to report")
		})
	}
}

// TestScrape_BodyOverCapIsRefusedNotTruncated is the resolved-question
// acceptance test: a body past the 5 MB cap must be REFUSED with the cap named
// in the output, never partially parsed. The prefix deliberately contains a
// series that would otherwise match and grade Up — so a truncating
// implementation would return StatusUp with value 1 and fail this test.
func TestScrape_BodyOverCapIsRefusedNotTruncated(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("# TYPE canary gauge\ncanary 1\n"))

		// Pad well past the cap with valid, ignorable comment lines.
		padding := strings.Repeat("# filler comment line that the exposition parser ignores entirely\n", 40000)
		for range 3 {
			_, _ = w.Write([]byte(padding))
		}
	}))
	t.Cleanup(server.Close)

	result := run(t.Context(), t, &checkprometheus.PrometheusConfig{
		URL: server.URL, Metric: "canary", Operator: ">", CriticalValue: f64(1e9),
	})

	r.Equal(checkerdef.StatusDown, result.Status)
	r.Nil(result.Metrics, "an over-cap body must not produce a graded value")

	message := fmt.Sprint(result.Output[checkerdef.OutputKeyError])
	r.Contains(message, "5 MB")
	r.Contains(message, "refused")
	r.Contains(message, "promql")
}

// TestScrape_UnderCapStillWorks is the positive control for the cap: a body
// just as long but under the limit parses normally.
func TestScrape_UnderCapStillWorks(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	body := "# TYPE canary gauge\ncanary 7\n" +
		strings.Repeat("# filler comment line that the exposition parser ignores entirely\n", 40000)
	r.Less(len(body), checkprometheus.MaxScrapeBytes)

	server, _ := metricsServer(t, body)

	result := run(t.Context(), t, &checkprometheus.PrometheusConfig{
		URL: server.URL, Metric: "canary", Operator: ">", CriticalValue: f64(1e9),
	})

	r.Equal(checkerdef.StatusUp, result.Status)
	r.InDelta(7.0, result.Metrics["value"], 1e-9)
}

// TestScrape_HeadersAreSent is a positive control: the fake server asserts the
// configured headers actually arrived, and the check only succeeds when the
// server accepted them.
func TestScrape_HeadersAreSent(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server, lastHeader := metricsServer(t, sampleExposition)

	result := run(t.Context(), t, &checkprometheus.PrometheusConfig{
		URL: server.URL, Metric: "process_open_fds", Operator: ">", CriticalValue: f64(1e9),
		Headers: map[string]string{"Authorization": "Bearer s3cret", "X-Scope-Orgid": "team-a"},
	})

	r.Equal(checkerdef.StatusUp, result.Status)

	header := lastHeader()
	r.NotNil(header)
	r.Equal("Bearer s3cret", header.Get("Authorization"))
	r.Equal("team-a", header.Get("X-Scope-Orgid"))
	r.NotEmpty(header.Get("User-Agent"))
}

// TestScrape_HeadersAreRequired is the negative half of the header control:
// a gated endpoint fails without the header and succeeds with it, so the
// positive control above cannot pass vacuously.
func TestScrape_HeadersAreRequired(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer s3cret" {
			w.WriteHeader(http.StatusUnauthorized)

			return
		}

		_, _ = w.Write([]byte(sampleExposition))
	}))
	t.Cleanup(server.Close)

	withoutHeader := run(t.Context(), t, &checkprometheus.PrometheusConfig{
		URL: server.URL, Metric: "process_open_fds", Operator: ">", CriticalValue: f64(1e9),
	})
	r.Equal(checkerdef.StatusDown, withoutHeader.Status)
	r.Contains(fmt.Sprint(withoutHeader.Output[checkerdef.OutputKeyError]), "401")

	withHeader := run(t.Context(), t, &checkprometheus.PrometheusConfig{
		URL: server.URL, Metric: "process_open_fds", Operator: ">", CriticalValue: f64(1e9),
		Headers: map[string]string{"Authorization": "Bearer s3cret"},
	})
	r.Equal(checkerdef.StatusUp, withHeader.Status)
}

func TestScrape_TransportFailures(t *testing.T) {
	t.Parallel()

	t.Run("non-200", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(server.Close)

		result := run(t.Context(), t, &checkprometheus.PrometheusConfig{
			URL: server.URL, Metric: "m", Operator: ">", CriticalValue: f64(1),
		})
		r.Equal(checkerdef.StatusDown, result.Status)
		r.Contains(fmt.Sprint(result.Output[checkerdef.OutputKeyError]), "500")
	})

	t.Run("unparseable body", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		server, _ := metricsServer(t, "this is definitely { not } exposition format\n")

		result := run(t.Context(), t, &checkprometheus.PrometheusConfig{
			URL: server.URL, Metric: "m", Operator: ">", CriticalValue: f64(1),
		})
		r.Equal(checkerdef.StatusDown, result.Status)
		r.Contains(fmt.Sprint(result.Output[checkerdef.OutputKeyError]), "parse")
	})

	t.Run("unreachable", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)

		server, _ := metricsServer(t, sampleExposition)
		url := server.URL
		server.Close()

		result := run(t.Context(), t, &checkprometheus.PrometheusConfig{
			URL: url, Metric: "process_open_fds", Operator: ">", CriticalValue: f64(1),
		})
		r.Equal(checkerdef.StatusDown, result.Status)
		r.NotEmpty(result.Output[checkerdef.OutputKeyError])
	})
}

// TestExecute_ContextCancellation covers both modes: a canceled context must
// surface as a failed result, never hang or panic.
func TestExecute_ContextCancellation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  func(url string) *checkprometheus.PrometheusConfig
	}{
		{
			name: "scrape",
			cfg: func(url string) *checkprometheus.PrometheusConfig {
				return &checkprometheus.PrometheusConfig{
					URL: url, Metric: "process_open_fds", Operator: ">", CriticalValue: f64(1),
				}
			},
		},
		{
			name: "promql",
			cfg: func(url string) *checkprometheus.PrometheusConfig {
				return &checkprometheus.PrometheusConfig{
					URL: url, Mode: checkprometheus.ModePromQL, Query: "up",
					Operator: ">", CriticalValue: f64(1),
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			server, _ := metricsServer(t, sampleExposition)

			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			result := run(ctx, t, tt.cfg(server.URL))
			r.NotEqual(checkerdef.StatusUp, result.Status)
			r.NotEmpty(result.Output[checkerdef.OutputKeyError])
		})
	}
}

// promServer serves a canned /api/v1/query response and records the query it
// was asked for.
func promServer(t *testing.T, status int, body string) (*httptest.Server, func() string) {
	t.Helper()

	var (
		mu    sync.Mutex
		query string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		query = req.URL.Query().Get("query")
		mu.Unlock()

		if req.URL.Path != "/api/v1/query" {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	return server, func() string {
		mu.Lock()
		defer mu.Unlock()

		return query
	}
}

func TestPromQL_Results(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     int
		body       string
		match      string
		onMissing  string
		critical   *float64
		wantStatus checkerdef.Status
		wantValue  *float64
		wantErr    string
	}{
		{
			name:   "instant vector single sample",
			status: http.StatusOK,
			body: `{"status":"success","data":{"resultType":"vector","result":[` +
				`{"metric":{"job":"api"},"value":[1700000000,"7.5"]}]}}`,
			critical: f64(1e9), wantStatus: checkerdef.StatusUp, wantValue: f64(7.5),
		},
		{
			name:     "scalar result",
			status:   http.StatusOK,
			body:     `{"status":"success","data":{"resultType":"scalar","result":[1700000000,"3"]}}`,
			critical: f64(1e9), wantStatus: checkerdef.StatusUp, wantValue: f64(3),
		},
		{
			name:   "breaching critical",
			status: http.StatusOK,
			body: `{"status":"success","data":{"resultType":"vector","result":[` +
				`{"metric":{},"value":[1700000000,"99"]}]}}`,
			critical: f64(10), wantStatus: checkerdef.StatusDown, wantValue: f64(99),
		},
		{
			name:      "empty vector honors onMissing up",
			status:    http.StatusOK,
			body:      `{"status":"success","data":{"resultType":"vector","result":[]}}`,
			onMissing: "up", critical: f64(1), wantStatus: checkerdef.StatusUp,
		},
		{
			name:     "empty vector defaults to down",
			status:   http.StatusOK,
			body:     `{"status":"success","data":{"resultType":"vector","result":[]}}`,
			critical: f64(1), wantStatus: checkerdef.StatusDown, wantErr: "no series matched",
		},
		{
			name:   "matrix result rejected",
			status: http.StatusOK,
			body: `{"status":"success","data":{"resultType":"matrix","result":[` +
				`{"metric":{},"values":[[1700000000,"1"],[1700000060,"2"]]}]}}`,
			critical: f64(1), wantStatus: checkerdef.StatusDown, wantErr: "matrix",
		},
		{
			name:     "string result rejected",
			status:   http.StatusOK,
			body:     `{"status":"success","data":{"resultType":"string","result":[1700000000,"hello"]}}`,
			critical: f64(1), wantStatus: checkerdef.StatusDown, wantErr: "string",
		},
		{
			name:   "query error surfaced",
			status: http.StatusBadRequest,
			body: `{"status":"error","errorType":"bad_data",` +
				`"error":"parse error: unexpected end of input"}`,
			critical: f64(1), wantStatus: checkerdef.StatusDown, wantErr: "parse error",
		},
		{
			name:   "multi-sample vector with single errors",
			status: http.StatusOK,
			body: `{"status":"success","data":{"resultType":"vector","result":[` +
				`{"metric":{"job":"a"},"value":[1700000000,"1"]},` +
				`{"metric":{"job":"b"},"value":[1700000000,"2"]}]}}`,
			critical: f64(1e9), wantStatus: checkerdef.StatusDown, wantErr: "multiple series",
		},
		{
			name:   "multi-sample vector aggregated with sum",
			status: http.StatusOK,
			body: `{"status":"success","data":{"resultType":"vector","result":[` +
				`{"metric":{"job":"a"},"value":[1700000000,"1"]},` +
				`{"metric":{"job":"b"},"value":[1700000000,"2"]}]}}`,
			match: "sum", critical: f64(1e9),
			wantStatus: checkerdef.StatusUp, wantValue: f64(3),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			server, lastQuery := promServer(t, tt.status, tt.body)

			result := run(t.Context(), t, &checkprometheus.PrometheusConfig{
				URL: server.URL, Mode: checkprometheus.ModePromQL,
				Query: `sum(rate(http_requests_total{code=~"5.."}[5m]))`,
				Match: tt.match, OnMissing: tt.onMissing,
				Operator: ">", CriticalValue: tt.critical,
			})

			r.Equal(tt.wantStatus, result.Status, "output: %v", result.Output)
			r.Equal("promql", result.Output["mode"])
			r.Equal(`sum(rate(http_requests_total{code=~"5.."}[5m]))`, lastQuery(),
				"the query must reach the server intact")

			if tt.wantValue != nil {
				r.InDelta(*tt.wantValue, result.Metrics["value"], 1e-9)
			} else {
				r.Nil(result.Metrics)
			}

			if tt.wantErr != "" {
				r.Contains(fmt.Sprint(result.Output[checkerdef.OutputKeyError]), tt.wantErr)
			}
		})
	}
}

// TestPromQL_BaseURLWithPathPrefix pins that a Prometheus served under a path
// prefix keeps that prefix instead of having it replaced.
func TestPromQL_BaseURLWithPathPrefix(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var (
		mu   sync.Mutex
		path string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		path = req.URL.Path
		mu.Unlock()

		_, _ = w.Write([]byte(
			`{"status":"success","data":{"resultType":"scalar","result":[1700000000,"1"]}}`,
		))
	}))
	t.Cleanup(server.Close)

	result := run(t.Context(), t, &checkprometheus.PrometheusConfig{
		URL: server.URL + "/prom", Mode: checkprometheus.ModePromQL, Query: "up",
		Operator: ">", CriticalValue: f64(1e9),
	})

	r.Equal(checkerdef.StatusUp, result.Status)

	mu.Lock()
	defer mu.Unlock()
	r.Equal("/prom/api/v1/query", path)
}

// TestExecute_HonorsTunnelDialer proves SupportsTunnel is real: with a dialer
// on the context, the request goes through it rather than dialing directly.
func TestExecute_HonorsTunnelDialer(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	server, _ := metricsServer(t, sampleExposition)

	var dialed int

	var mu sync.Mutex

	dialer := checkerdef.DialerFunc(func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		dialed++
		mu.Unlock()

		return (&net.Dialer{}).DialContext(ctx, network, addr)
	})

	ctx := checkerdef.WithTunnelDialer(t.Context(), dialer)

	result := run(ctx, t, &checkprometheus.PrometheusConfig{
		URL: server.URL, Metric: "process_open_fds", Operator: ">", CriticalValue: f64(1e9),
	})

	r.Equal(checkerdef.StatusUp, result.Status)

	mu.Lock()
	defer mu.Unlock()
	r.Positive(dialed, "the tunnel dialer must be used")
}
