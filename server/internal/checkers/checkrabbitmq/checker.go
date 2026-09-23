// Package checkrabbitmq provides RabbitMQ server health checks.
package checkrabbitmq

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkrabbitmq/config"
)

const microsecondsPerMilli = 1000.0

// RabbitMQChecker implements the Checker interface for RabbitMQ health checks.
type RabbitMQChecker struct{}

// Type returns the check type identifier.
func (c *RabbitMQChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeRabbitMQ
}

// Validate checks if the configuration is valid. Every rule lives in the light
// `config` sub-package so an offline validator (`sp checks validate`) can run it
// without linking this checker's execution client.
func (c *RabbitMQChecker) Validate(spec *checkerdef.CheckSpec) error {
	return checkconfig.ValidateSpec(spec)
}

// Execute performs the RabbitMQ check and returns the result.
func (c *RabbitMQChecker) Execute(
	ctx context.Context,
	config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*RabbitMQConfig](config)
	if err != nil {
		return nil, err
	}

	mode := cfg.Mode
	if mode == "" {
		mode = defaultMode
	}

	if mode == ModeManagement {
		return c.executeManagement(ctx, cfg)
	}

	return c.executeAMQP(ctx, cfg)
}

// executeAMQP performs AMQP protocol health check.
func (c *RabbitMQChecker) executeAMQP(
	ctx context.Context,
	cfg *RabbitMQConfig,
) (*checkerdef.Result, error) {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	port := cfg.Port
	if port == 0 {
		port = defaultPort
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	metrics := map[string]any{}
	output := map[string]any{
		checkerdef.OutputKeyHost: cfg.Host,
		checkerdef.OutputKeyPort: port,
		"mode":                   ModeAMQP,
	}

	result := c.dialAndCheck(ctx, cfg, start, metrics, output)

	return result, nil
}

func (c *RabbitMQChecker) dialAndCheck(
	ctx context.Context,
	cfg *RabbitMQConfig,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	uri := cfg.BuildAMQPURI()

	amqpConfig := amqp.Config{}
	if cfg.TLS {
		amqpConfig.TLSClientConfig = &tls.Config{
			ServerName: cfg.Host,
		}
	}

	// Tunneled: dial through the bastion. amqp091-go hands the raw host:port from
	// the URI to this Dial func (no local resolution), so the bastion resolves
	// the hostname. We replicate amqp.DefaultDial's handshake deadline so a dead
	// server cannot stall the AMQP handshake. Untunneled, Dial stays nil and the
	// library uses its default dialer byte-for-byte.
	if dialer := checkerdef.TunnelDialerFrom(ctx); dialer != nil {
		timeout := cfg.Timeout
		if timeout == 0 {
			timeout = defaultTimeout
		}

		amqpConfig.Dial = func(network, addr string) (net.Conn, error) {
			conn, dialErr := dialer.DialContext(ctx, network, addr)
			if dialErr != nil {
				return nil, dialErr
			}

			_ = conn.SetDeadline(time.Now().Add(timeout))

			return conn, nil
		}

		output["tunneled"] = true
	}

	conn, err := amqp.DialConfig(uri, amqpConfig)
	if err != nil {
		return handleAMQPError(ctx, err, start, "connection failed", output)
	}

	defer func() { _ = conn.Close() }()

	metrics["connection_time_ms"] = durationMs(time.Since(start))

	channel, err := conn.Channel()
	if err != nil {
		return handleAMQPError(ctx, err, start, "channel open failed", output)
	}

	defer func() { _ = channel.Close() }()

	if cfg.Queue != "" {
		if result := c.checkQueue(ctx, channel, cfg.Queue, start, metrics, output); result != nil {
			return result
		}
	}

	metrics["total_time_ms"] = durationMs(time.Since(start))

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}
}

func (c *RabbitMQChecker) checkQueue(
	ctx context.Context,
	channel *amqp.Channel,
	queue string,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	queueInfo, err := channel.QueueDeclarePassive(queue, false, false, false, false, nil)
	if err != nil {
		return handleAMQPError(ctx, err, start, "queue inspect failed", output)
	}

	output["queue"] = queue
	metrics["queue_messages"] = queueInfo.Messages
	metrics["queue_consumers"] = queueInfo.Consumers

	return nil
}

// executeManagement performs HTTP management API health check.
func (c *RabbitMQChecker) executeManagement(
	ctx context.Context,
	cfg *RabbitMQConfig,
) (*checkerdef.Result, error) {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	mgmtPort := cfg.ManagementPort
	if mgmtPort == 0 {
		mgmtPort = defaultManagementPort
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	output := map[string]any{
		checkerdef.OutputKeyHost: cfg.Host,
		checkerdef.OutputKeyPort: mgmtPort,
		"mode":                   ModeManagement,
	}

	alarms, err := c.doManagementRequest(ctx, cfg, mgmtPort, start, mergeOutput(output, nil))
	if err != nil {
		return alarms, err
	}

	return c.applyNodeThresholds(ctx, cfg, mgmtPort, start, alarms), nil
}

// applyNodeThresholds fetches /api/nodes (every management-mode execution,
// thresholds configured or not) and layers the memory/disk evaluation on top
// of the alarms-probe result:
//   - the alarms probe stays authoritative for an already-active resource
//     alarm — a non-Up alarms status always wins;
//   - otherwise, the node-threshold evaluation decides (Up unless a
//     threshold is configured and breached, or the /api/nodes call itself
//     failed while thresholds are configured, which is treated as Down since
//     the thresholds can no longer be verified).
//
// Metrics (mem/disk gauges, nodes_running/nodes_total) are recorded whenever
// the nodes fetch succeeds, independent of whether any threshold is set, so
// existing checks gain history for free.
func (c *RabbitMQChecker) applyNodeThresholds(
	ctx context.Context,
	cfg *RabbitMQConfig,
	mgmtPort int,
	start time.Time,
	alarms *checkerdef.Result,
) *checkerdef.Result {
	memWarn, memCrit, diskWarn, diskCrit := cfg.ResolveThresholds()
	thresholdsConfigured := memWarn != nil || memCrit != nil || diskWarn != nil || diskCrit != nil

	nodes, err := fetchNodes(ctx, cfg, mgmtPort)

	output := mergeOutput(alarms.Output, nil)
	metrics := make(map[string]any, len(alarms.Metrics))

	for k, v := range alarms.Metrics {
		metrics[k] = v
	}

	nodeStatus := checkerdef.StatusUp

	var breachMessage string

	switch {
	case err != nil:
		output["nodes_error"] = err.Error()

		if thresholdsConfigured {
			nodeStatus = checkerdef.StatusDown
			breachMessage = "cannot evaluate memory/disk thresholds: " + err.Error()
		}
	default:
		nodeStatus, breachMessage = gradeNodes(nodes, memWarn, memCrit, diskWarn, diskCrit, output, metrics)
	}

	status := alarms.Status
	if status == checkerdef.StatusUp {
		status = nodeStatus
	}

	if status != checkerdef.StatusUp && output[checkerdef.OutputKeyError] == nil && breachMessage != "" {
		output[checkerdef.OutputKeyError] = breachMessage
	}

	return &checkerdef.Result{
		Status:   status,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}
}

// gradeNodes runs the per-node threshold evaluation once /api/nodes has been
// fetched successfully: it writes the raw per-node output and the metrics
// gauges into output/metrics (mutated in place) and returns the resulting
// node-threshold status plus the worst node's breach message (empty when
// nothing breached).
func gradeNodes(
	nodes []nodeInfo, memWarn, memCrit, diskWarn, diskCrit *threshold, output, metrics map[string]any,
) (checkerdef.Status, string) {
	running := runningNodes(nodes)
	output["nodes"] = nodeOutputs(running)

	if len(running) == 0 {
		return checkerdef.StatusUp, ""
	}

	evals := make([]nodeEval, 0, len(running))
	for i := range running {
		evals = append(evals, evaluateNode(running[i], memWarn, memCrit, diskWarn, diskCrit))
	}

	worst := worstEval(evals)

	// notes carries every node's reasons — not just the worst node's — so an
	// informational note (e.g. "mem_limit is not reported") on a node that
	// is otherwise fine still surfaces even when the overall status stays Up
	// and nothing goes into `error`.
	var notes []string
	for i := range evals {
		notes = append(notes, evals[i].reasons...)
	}

	if len(notes) > 0 {
		output["notes"] = notes
	}

	metrics["mem_used_bytes"] = float64(worst.node.MemUsed)
	metrics["mem_limit_bytes"] = float64(worst.node.MemLimit)
	metrics["mem_used_percent"] = worst.memPercent
	metrics["disk_free_bytes"] = float64(worst.node.DiskFree)
	metrics["disk_free_limit_bytes"] = float64(worst.node.DiskFreeLimit)
	metrics["nodes_running"] = float64(len(running))
	metrics["nodes_total"] = float64(len(nodes))

	return worst.status, strings.Join(worst.reasons, "; ")
}

// nodeInfo is the subset of RabbitMQ's `GET /api/nodes` response this check
// reads. Field names confirmed against the RabbitMQ management HTTP API
// (management API docs / `rabbitmqctl node_health_check` overview): each
// node object carries `running`, `mem_used`, `mem_limit`, `mem_alarm`,
// `disk_free`, `disk_free_limit`, `disk_free_alarm` alongside many other
// stats this check does not need.
//
//nolint:tagliatelle // JSON tags must match the RabbitMQ management API field names
type nodeInfo struct {
	Name          string `json:"name"`
	Running       bool   `json:"running"`
	MemUsed       int64  `json:"mem_used"`
	MemLimit      int64  `json:"mem_limit"`
	MemAlarm      bool   `json:"mem_alarm"`
	DiskFree      int64  `json:"disk_free"`
	DiskFreeLimit int64  `json:"disk_free_limit"`
	DiskFreeAlarm bool   `json:"disk_free_alarm"`
}

// errNodesRequestFailed wraps a non-200 /api/nodes response into a static,
// wrapped error (err113) while still naming the actual status/body.
var errNodesRequestFailed = errors.New("management API request failed")

// fetchNodes calls GET /api/nodes with the same credentials/timeout budget
// as the alarms probe.
func fetchNodes(ctx context.Context, cfg *RabbitMQConfig, mgmtPort int) ([]nodeInfo, error) {
	scheme := "http"
	if cfg.TLS {
		scheme = "https"
	}

	nodesURL := fmt.Sprintf("%s://%s:%d/api/nodes", scheme, cfg.Host, mgmtPort)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nodesURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(cfg.Username, cfg.Password)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d: %s", errNodesRequestFailed, resp.StatusCode, string(body))
	}

	var nodes []nodeInfo
	if err := json.Unmarshal(body, &nodes); err != nil {
		return nil, fmt.Errorf("failed to parse /api/nodes response: %w", err)
	}

	return nodes, nil
}

// runningNodes filters to the nodes RabbitMQ reports as running — a
// non-running node has no meaningful mem/disk figures to evaluate.
func runningNodes(nodes []nodeInfo) []nodeInfo {
	running := make([]nodeInfo, 0, len(nodes))

	for i := range nodes {
		if nodes[i].Running {
			running = append(running, nodes[i])
		}
	}

	return running
}

// nodeOutputs renders the raw per-node fields for the result output.
func nodeOutputs(nodes []nodeInfo) []map[string]any {
	out := make([]map[string]any, 0, len(nodes))

	for i := range nodes {
		n := &nodes[i]
		out = append(out, map[string]any{
			"name":            n.Name,
			"mem_used":        n.MemUsed,
			"mem_limit":       n.MemLimit,
			"mem_alarm":       n.MemAlarm,
			"disk_free":       n.DiskFree,
			"disk_free_limit": n.DiskFreeLimit,
			"disk_free_alarm": n.DiskFreeAlarm,
		})
	}

	return out
}

const (
	tierWarning  = "warning"
	tierCritical = "critical"

	percentScale = 100.0
)

// nodeEval is one node's threshold evaluation.
type nodeEval struct {
	node         nodeInfo
	status       checkerdef.Status
	reasons      []string
	memPercent   float64
	memPercentOK bool
}

// evaluateNode grades a single node against the configured thresholds. It is
// safe to call with every threshold nil (no thresholds configured): every
// comparison is then a no-op and the node reports StatusUp, but memPercent is
// still computed so an unconfigured check's "worst node" selection reflects
// genuine memory/disk pressure rather than an arbitrary node.
func evaluateNode(node nodeInfo, memWarn, memCrit, diskWarn, diskCrit *threshold) nodeEval {
	eval := nodeEval{node: node, status: checkerdef.StatusUp}

	if node.MemLimit > 0 {
		eval.memPercentOK = true
		eval.memPercent = float64(node.MemUsed) / float64(node.MemLimit) * percentScale
	} else if isPercentThreshold(memWarn) || isPercentThreshold(memCrit) {
		eval.reasons = append(eval.reasons,
			node.Name+": mem_limit is not reported, skipping the memory percent threshold")
	}

	memUsed := nonNegative(node.MemUsed)
	diskFree := nonNegative(node.DiskFree)

	if breach, ok := ceilingBreach(memUsed, node.MemLimit, memCrit); ok && breach {
		eval.status = checkerdef.StatusDown
		eval.reasons = append(eval.reasons,
			memMessage(node.Name, memUsed, node.MemLimit, tierCritical, memCrit, eval.memPercentOK, eval.memPercent))
	} else if breach, ok := ceilingBreach(memUsed, node.MemLimit, memWarn); ok && breach {
		eval.status = worseStatus(eval.status, checkerdef.StatusWarning)
		eval.reasons = append(eval.reasons,
			memMessage(node.Name, memUsed, node.MemLimit, tierWarning, memWarn, eval.memPercentOK, eval.memPercent))
	}

	if floorBreach(diskFree, diskCrit) {
		eval.status = checkerdef.StatusDown
		eval.reasons = append(eval.reasons, diskMessage(node.Name, diskFree, tierCritical, diskCrit))
	} else if floorBreach(diskFree, diskWarn) {
		eval.status = worseStatus(eval.status, checkerdef.StatusWarning)
		eval.reasons = append(eval.reasons, diskMessage(node.Name, diskFree, tierWarning, diskWarn))
	}

	return eval
}

// worstEval picks the node the check status/metrics should be based on: the
// worst graded status first, then (ties, or nothing configured) the node
// under the most memory pressure, then the node with the least free disk,
// then node name for full determinism.
func worstEval(evals []nodeEval) nodeEval {
	worst := &evals[0]

	for i := 1; i < len(evals); i++ {
		if isWorseEval(&evals[i], worst) {
			worst = &evals[i]
		}
	}

	return *worst
}

func isWorseEval(evalA, evalB *nodeEval) bool {
	rankA, rankB := statusRank(evalA.status), statusRank(evalB.status)
	if rankA != rankB {
		return rankA > rankB
	}

	if evalA.memPercentOK && evalB.memPercentOK && evalA.memPercent != evalB.memPercent {
		return evalA.memPercent > evalB.memPercent
	}

	if evalA.node.DiskFree != evalB.node.DiskFree {
		return evalA.node.DiskFree < evalB.node.DiskFree
	}

	return evalA.node.Name < evalB.node.Name
}

// statusRank orders the three statuses this feature ever assigns a node,
// worst last. checkerdef.Status.Severity() cannot be reused here: it ranks
// StatusWarning level with StatusUp (by design, for availability), while
// this feature needs Warning to sit strictly between Up and Down.
//
//nolint:exhaustive // evaluateNode only ever produces Up/Warning/Down; every other status ranks as Up.
func statusRank(s checkerdef.Status) int {
	switch s {
	case checkerdef.StatusDown:
		return 2
	case checkerdef.StatusWarning:
		return 1
	default:
		return 0
	}
}

func worseStatus(a, b checkerdef.Status) checkerdef.Status {
	if statusRank(b) > statusRank(a) {
		return b
	}

	return a
}

func isPercentThreshold(t *threshold) bool {
	return t != nil && t.IsPercent
}

// nonNegative clamps a management-API counter to a safe uint64: RabbitMQ
// never reports a negative byte count, but a malformed/unexpected value must
// not underflow into a huge uint64 and manufacture a false breach.
func nonNegative(v int64) uint64 {
	if v < 0 {
		return 0
	}

	return uint64(v)
}

// ceilingBreach reports whether `used` breaches a ceiling threshold (a
// percentage of `limit`, or an absolute byte count) — the shape memory
// thresholds take. ok is false when the comparison could not be made (a
// percent threshold with a non-positive limit); the caller must not treat
// that as "not breached".
func ceilingBreach(used uint64, limit int64, thr *threshold) (bool, bool) {
	if thr == nil {
		return false, true
	}

	if thr.IsPercent {
		if limit <= 0 {
			return false, false
		}

		pct := float64(used) / float64(limit) * percentScale

		return pct >= float64(thr.Percent), true
	}

	return used >= thr.Bytes, true
}

// floorBreach reports whether `free` breaches a floor threshold (bytes
// only) — the shape disk thresholds take: breach when free <= threshold.
func floorBreach(free uint64, thr *threshold) bool {
	if thr == nil {
		return false
	}

	return free <= thr.Bytes
}

func memMessage(
	nodeName string, used uint64, limit int64, tier string, thr *threshold, percentOK bool, percent float64,
) string {
	usedStr := humanize.IBytes(used)

	if thr.IsPercent && percentOK {
		return fmt.Sprintf(
			"memory used %s is %.0f%% of the %s high watermark on %s (%s threshold %s)",
			usedStr, percent, humanize.IBytes(nonNegative(limit)), nodeName, tier, thr.Raw,
		)
	}

	return fmt.Sprintf("memory used %s on %s breaches the %s threshold %s", usedStr, nodeName, tier, thr.Raw)
}

func diskMessage(nodeName string, free uint64, tier string, thr *threshold) string {
	return fmt.Sprintf(
		"disk free %s on %s is below the %s threshold %s", humanize.IBytes(free), nodeName, tier, thr.Raw,
	)
}

func (c *RabbitMQChecker) doManagementRequest(
	ctx context.Context,
	cfg *RabbitMQConfig,
	mgmtPort int,
	start time.Time,
	output map[string]any,
) (*checkerdef.Result, error) {
	scheme := "http"
	if cfg.TLS {
		scheme = "https"
	}

	healthURL := fmt.Sprintf("%s://%s:%d/api/health/checks/alarms", scheme, cfg.Host, mgmtPort)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output:   mergeOutput(output, map[string]any{checkerdef.OutputKeyError: "failed to create request: " + err.Error()}),
		}, nil
	}

	req.SetBasicAuth(cfg.Username, cfg.Password)

	metrics := map[string]any{}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return handleManagementError(ctx, err, start, output), nil
	}

	defer func() { _ = resp.Body.Close() }()

	metrics["connection_time_ms"] = durationMs(time.Since(start))

	body, _ := io.ReadAll(resp.Body)
	metrics["total_time_ms"] = durationMs(time.Since(start))

	if resp.StatusCode != http.StatusOK {
		output["status_code"] = resp.StatusCode
		output[checkerdef.OutputKeyError] = fmt.Sprintf(
			"management API returned status %d: %s", resp.StatusCode, string(body),
		)

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}, nil
	}

	output["status_code"] = resp.StatusCode

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}, nil
}

func handleAMQPError(
	ctx context.Context,
	err error,
	start time.Time,
	prefix string,
	output map[string]any,
) *checkerdef.Result {
	if ctx.Err() != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: time.Since(start),
			Output:   mergeOutput(output, map[string]any{checkerdef.OutputKeyError: "connection timeout"}),
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Output:   mergeOutput(output, map[string]any{checkerdef.OutputKeyError: fmt.Sprintf("%s: %v", prefix, err)}),
	}
}

func handleManagementError(
	ctx context.Context,
	err error,
	start time.Time,
	output map[string]any,
) *checkerdef.Result {
	if ctx.Err() != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: time.Since(start),
			Output:   mergeOutput(output, map[string]any{checkerdef.OutputKeyError: "request timeout"}),
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Output:   mergeOutput(output, map[string]any{checkerdef.OutputKeyError: fmt.Sprintf("request failed: %v", err)}),
	}
}

func durationMs(d time.Duration) float64 {
	return float64(d.Microseconds()) / microsecondsPerMilli
}

func mergeOutput(base, extra map[string]any) map[string]any {
	result := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		result[k] = v
	}

	for k, v := range extra {
		result[k] = v
	}

	return result
}
