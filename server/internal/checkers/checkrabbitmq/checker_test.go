package checkrabbitmq_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkrabbitmq"
)

// managementServer fakes the two endpoints checkrabbitmq's management mode
// calls: /api/health/checks/alarms (alarmsStatus/alarmsBody) and /api/nodes
// (nodesStatus/nodesBody). Deterministic, no live broker.
type managementServer struct {
	alarmsStatus int
	alarmsBody   string
	nodesStatus  int
	nodesBody    string
}

func (s managementServer) start(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/health/checks/alarms"):
			status := s.alarmsStatus
			if status == 0 {
				status = http.StatusOK
			}

			w.WriteHeader(status)

			body := s.alarmsBody
			if body == "" {
				body = `{"status":"ok"}`
			}

			_, _ = w.Write([]byte(body))
		case strings.HasSuffix(r.URL.Path, "/api/nodes"):
			status := s.nodesStatus
			if status == 0 {
				status = http.StatusOK
			}

			w.WriteHeader(status)
			_, _ = w.Write([]byte(s.nodesBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

// mem_limit is a clean 1e9 across every fixture so a "70%"/"90%" threshold
// lands on an exact mem_used value with no floating-point rounding surprises.
const oneNodeUnderThreshold = `[
	{"name":"rabbit@node1","running":true,"mem_used":250000000,"mem_limit":1000000000,
	 "mem_alarm":false,"disk_free":21474836480,"disk_free_limit":1073741824,"disk_free_alarm":false}
]`

const oneNodeMemoryWarning = `[
	{"name":"rabbit@node1","running":true,"mem_used":700000000,"mem_limit":1000000000,
	 "mem_alarm":false,"disk_free":21474836480,"disk_free_limit":1073741824,"disk_free_alarm":false}
]`

const oneNodeMemoryCritical = `[
	{"name":"rabbit@node1","running":true,"mem_used":900000000,"mem_limit":1000000000,
	 "mem_alarm":false,"disk_free":21474836480,"disk_free_limit":1073741824,"disk_free_alarm":false}
]`

// runManagementCheck builds and executes a management-mode config against a
// fake server's address.
func runManagementCheck(t *testing.T, srv *httptest.Server, extra map[string]any) *checkerdef.Result {
	t.Helper()

	r := require.New(t)

	parsed, err := url.Parse(srv.URL)
	r.NoError(err)

	host, portStr, err := net.SplitHostPort(parsed.Host)
	r.NoError(err)

	port, err := strconv.Atoi(portStr)
	r.NoError(err)

	cfgMap := map[string]any{
		"host":           host,
		"username":       "guest",
		"password":       "guest",
		"mode":           checkrabbitmq.ModeManagement,
		"managementPort": float64(port),
	}

	for k, v := range extra {
		cfgMap[k] = v
	}

	cfg := &checkrabbitmq.RabbitMQConfig{}
	r.NoError(cfg.FromMap(cfgMap))
	r.NoError(cfg.Validate())

	checker := &checkrabbitmq.RabbitMQChecker{}
	result, err := checker.Execute(t.Context(), cfg)
	r.NoError(err)

	return result
}

func TestExecuteManagement_MetricsRecordedEveryTime(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := managementServer{nodesBody: oneNodeUnderThreshold}.start(t)
	result := runManagementCheck(t, srv, nil)

	r.Equal(checkerdef.StatusUp, result.Status)
	r.InDelta(float64(250000000), result.Metrics["mem_used_bytes"], 0.5)
	r.InDelta(float64(1000000000), result.Metrics["mem_limit_bytes"], 0.5)
	r.InDelta(25.0, result.Metrics["mem_used_percent"], 0.01)
	r.InDelta(float64(21474836480), result.Metrics["disk_free_bytes"], 0.5)
	r.InDelta(float64(1), result.Metrics["nodes_running"], 0.5)
	r.InDelta(float64(1), result.Metrics["nodes_total"], 0.5)
}

func TestExecuteManagement_UnderThreshold(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := managementServer{nodesBody: oneNodeUnderThreshold}.start(t)
	result := runManagementCheck(t, srv, map[string]any{
		"memoryUsedWarning":  "70%",
		"memoryUsedCritical": "90%",
	})

	r.Equal(checkerdef.StatusUp, result.Status)
	r.Nil(result.Output[checkerdef.OutputKeyError])
}

func TestExecuteManagement_WarningBreach(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := managementServer{nodesBody: oneNodeMemoryWarning}.start(t)
	result := runManagementCheck(t, srv, map[string]any{
		"memoryUsedWarning":  "70%",
		"memoryUsedCritical": "90%",
	})

	r.Equal(checkerdef.StatusWarning, result.Status)
	r.Contains(result.Output[checkerdef.OutputKeyError], "warning")
}

// Positive control: a real breach really flips the status, not merely that
// the fields parsed without error.
func TestExecuteManagement_CriticalBreachFlipsStatusAndReportsHumanMessage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := managementServer{nodesBody: oneNodeMemoryCritical}.start(t)
	result := runManagementCheck(t, srv, map[string]any{
		"memoryUsedWarning":  "70%",
		"memoryUsedCritical": "90%",
	})

	r.Equal(checkerdef.StatusDown, result.Status)
	errMsg, _ := result.Output[checkerdef.OutputKeyError].(string)
	r.Contains(errMsg, "memory used")
	r.Contains(errMsg, "rabbit@node1")
	r.Contains(errMsg, "critical threshold 90%")
}

func TestExecuteManagement_ThreeNodeClusterWorstNodeWins(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cluster := `[
		{"name":"rabbit@a","running":true,"mem_used":100,"mem_limit":1000000000,
		 "mem_alarm":false,"disk_free":21474836480,"disk_free_limit":1073741824,"disk_free_alarm":false},
		{"name":"rabbit@b","running":true,"mem_used":900000000,"mem_limit":1000000000,
		 "mem_alarm":false,"disk_free":21474836480,"disk_free_limit":1073741824,"disk_free_alarm":false},
		{"name":"rabbit@c","running":true,"mem_used":200,"mem_limit":1000000000,
		 "mem_alarm":false,"disk_free":21474836480,"disk_free_limit":1073741824,"disk_free_alarm":false}
	]`

	srv := managementServer{nodesBody: cluster}.start(t)
	result := runManagementCheck(t, srv, map[string]any{
		"memoryUsedWarning":  "70%",
		"memoryUsedCritical": "90%",
	})

	r.Equal(checkerdef.StatusDown, result.Status)
	r.InDelta(float64(900000000), result.Metrics["mem_used_bytes"], 0.5)

	errMsg, _ := result.Output[checkerdef.OutputKeyError].(string)
	r.Contains(errMsg, "rabbit@b")
}

func TestExecuteManagement_NodesFetchFailsWithoutThresholds(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := managementServer{nodesStatus: http.StatusForbidden, nodesBody: "forbidden"}.start(t)
	result := runManagementCheck(t, srv, nil)

	r.Equal(checkerdef.StatusUp, result.Status)
	r.NotEmpty(result.Output["nodes_error"])
}

func TestExecuteManagement_NodesFetchFailsWithThresholds(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := managementServer{nodesStatus: http.StatusForbidden, nodesBody: "forbidden"}.start(t)
	result := runManagementCheck(t, srv, map[string]any{
		"memoryUsedCritical": "90%",
	})

	r.Equal(checkerdef.StatusDown, result.Status)
	r.NotEmpty(result.Output["nodes_error"])
}

func TestExecuteManagement_AlarmsProbeDominatesEvenUnderThreshold(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	srv := managementServer{alarmsStatus: http.StatusServiceUnavailable, nodesBody: oneNodeUnderThreshold}.start(t)
	result := runManagementCheck(t, srv, map[string]any{
		"memoryUsedCritical": "90%",
	})

	r.Equal(checkerdef.StatusDown, result.Status)
}

func TestExecuteManagement_ZeroMemLimitWithPercentThresholdDoesNotPanic(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	zeroLimit := `[
		{"name":"rabbit@node1","running":true,"mem_used":1000,"mem_limit":0,
		 "mem_alarm":false,"disk_free":21474836480,"disk_free_limit":1073741824,"disk_free_alarm":false}
	]`

	srv := managementServer{nodesBody: zeroLimit}.start(t)

	r.NotPanics(func() {
		result := runManagementCheck(t, srv, map[string]any{
			"memoryUsedCritical": "90%",
		})
		r.Equal(checkerdef.StatusUp, result.Status)
	})
}
