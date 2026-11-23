// Package checksystemd reports the state of a systemd unit of the host that
// runs the check, through `systemctl show` (no D-Bus client, no new
// dependency: systemctl is present wherever systemd is).
//
// The unit name is the security boundary: it is validated against a strict
// character set, never starts with `-`, is passed in an argv slice (no shell)
// and after `--`, so it can never be read as a systemctl flag.
//
// A unit only means something on the host that runs the check, so the type
// never runs through a tunnel, and an enforcing egress guard (a SaaS shared
// worker) refuses it before systemctl runs: such a worker must never report
// on its own host's units.
package checksystemd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checksystemd/config"
	"github.com/fclairamb/solidping/server/internal/egress"
)

const (
	defaultTimeout = 5 * time.Second
	systemctlBin   = "systemctl"
	maxStderr      = 512

	// timestampLayout is the format of systemctl's *Timestamp properties,
	// e.g. "Mon 2026-11-23 10:00:00 UTC".
	timestampLayout = "Mon 2006-01-02 15:04:05 MST"

	// OutputKeyUnit is the full unit name the check queried.
	OutputKeyUnit = "unit"
	// OutputKeyState is ActiveState/SubState, e.g. active/running.
	OutputKeyState = "state"
	// OutputKeyResult is the unit's Result property (success, exit-code...).
	OutputKeyResult = "result"
	// OutputKeyLoadState is the unit's LoadState (loaded, not-found...).
	OutputKeyLoadState = "load_state"

	metricActiveSince = "active_since_s"
)

// Properties the check reads.
const (
	propLoadState   = "LoadState"
	propActiveState = "ActiveState"
	propSubState    = "SubState"
	propResult      = "Result"
	propActiveEnter = "ActiveEnterTimestamp"
)

var (
	// ErrTunnelNotSupported is returned when a systemd check runs through a
	// tunnel: a unit means nothing on the far side of a bastion.
	ErrTunnelNotSupported = errors.New("systemd checks cannot run through a tunnel")
	// ErrSystemctlMissing is returned when the host has no systemctl.
	ErrSystemctlMissing = errors.New(
		"systemctl not found: this host does not run systemd (or SolidPing runs in a container without it)")
)

// Runner runs a command and returns its stdout. It is injectable so tests
// never need a real systemd.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// SystemdChecker implements the Checker interface for systemd units.
type SystemdChecker struct {
	// Run executes systemctl; nil means exec.CommandContext.
	Run Runner
	// Now is the clock used for active_since_s; nil means time.Now.
	Now func() time.Time
}

// Type returns the check type identifier.
func (c *SystemdChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeSystemd
}

// Validate checks the configuration.
func (c *SystemdChecker) Validate(spec *checkerdef.CheckSpec) error {
	return checkconfig.ValidateSpec(spec)
}

// Args returns the systemctl argv (without the binary) for a unit.
func Args(unit string) []string {
	return []string{
		"show",
		"--property=" + strings.Join([]string{propLoadState, propActiveState, propSubState, propResult, propActiveEnter}, ","),
		"--no-pager",
		"--",
		unit,
	}
}

// Execute runs `systemctl show` and grades the unit's state.
func (c *SystemdChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*SystemdConfig](config)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	unit := cfg.UnitName()

	if refusal := refuse(ctx, unit); refusal != nil {
		return finish(errorResult(refusal.Error()), unit, start), nil
	}

	// Defence in depth: the create path validated the unit already, but a
	// config may reach Execute another way (a js sub-check, a stale row).
	if validateErr := checkconfig.ValidateUnit(cfg.Unit); validateErr != nil {
		return finish(errorResult(validateErr.Error()), unit, start), nil
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}

	ctxWithTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, runErr := c.runner()(ctxWithTimeout, systemctlBin, Args(unit)...)
	if runErr != nil {
		return finish(runFailure(ctxWithTimeout, runErr), unit, start), nil
	}

	return finish(Grade(ParseShow(out), cfg.WantActive(), c.now()), unit, start), nil
}

func (c *SystemdChecker) runner() Runner {
	if c.Run != nil {
		return c.Run
	}

	return execRunner
}

func (c *SystemdChecker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}

	return time.Now()
}

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil && stderr.Len() > 0 {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > maxStderr {
			msg = msg[:maxStderr]
		}

		return out, fmt.Errorf("%w: %s", err, msg)
	}

	return out, err
}

// refuse returns the reason this execution must not run systemctl, or nil.
func refuse(ctx context.Context, unit string) error {
	if checkerdef.TunnelDialerFrom(ctx) != nil {
		return ErrTunnelNotSupported
	}

	if egress.FromContext(ctx).Enforcing() {
		denied := &egress.DeniedError{Host: "systemd:" + unit}
		egress.RecordDenial(ctx, denied)

		return denied
	}

	return nil
}

func errorResult(msg string) *checkerdef.Result {
	return &checkerdef.Result{
		Status: checkerdef.StatusError,
		Output: map[string]any{checkerdef.OutputKeyError: msg},
	}
}

// runFailure turns a systemctl failure into a result. A missing systemctl or
// a host without systemd is an error (the check cannot tell), not down.
func runFailure(ctx context.Context, err error) *checkerdef.Result {
	if errors.Is(err, exec.ErrNotFound) {
		return errorResult(ErrSystemctlMissing.Error())
	}

	if ctx.Err() != nil {
		return &checkerdef.Result{
			Status: checkerdef.StatusTimeout,
			Output: map[string]any{checkerdef.OutputKeyError: "systemctl timed out"},
		}
	}

	return errorResult(fmt.Sprintf("systemctl failed: %v", err))
}

func finish(result *checkerdef.Result, unit string, start time.Time) *checkerdef.Result {
	result.Duration = time.Since(start)

	if result.Output == nil {
		result.Output = map[string]any{}
	}

	result.Output[OutputKeyUnit] = unit

	return result
}

// ParseShow parses the KEY=value lines of `systemctl show`.
func ParseShow(out []byte) map[string]string {
	props := map[string]string{}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if ok {
			props[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}

	return props
}

// isRunning reports whether an ActiveState counts as running: a unit
// restarting under Restart= (activating) or reloading is not an outage.
func isRunning(activeState string) bool {
	switch activeState {
	case "active", "activating", "reloading", "refreshing":
		return true
	default:
		return false
	}
}

// Grade maps the parsed properties to a result. wantActive false inverts
// up/down: the unit must not run.
func Grade(props map[string]string, wantActive bool, now time.Time) *checkerdef.Result {
	output := map[string]any{
		OutputKeyLoadState: props[propLoadState],
	}

	if props[propLoadState] == "not-found" {
		output[checkerdef.OutputKeyError] = "unit not found"

		return &checkerdef.Result{Status: checkerdef.StatusDown, Output: output}
	}

	activeState := props[propActiveState]
	if activeState == "" {
		output[checkerdef.OutputKeyError] = "systemctl returned no ActiveState"

		return &checkerdef.Result{Status: checkerdef.StatusError, Output: output}
	}

	state := activeState + "/" + props[propSubState]
	output[OutputKeyState] = state

	if result := props[propResult]; result != "" {
		output[OutputKeyResult] = result
	}

	metrics := map[string]any{}

	if activeState == "active" {
		if since, ok := activeSince(props[propActiveEnter], now); ok {
			metrics[metricActiveSince] = since
		}
	}

	status := checkerdef.StatusUp

	if isRunning(activeState) != wantActive {
		status = checkerdef.StatusDown

		expected := checkconfig.StateActive
		if !wantActive {
			expected = checkconfig.StateInactive
		}

		output[checkerdef.OutputKeyError] = fmt.Sprintf("unit is %s, expected %s", state, expected)
	}

	return &checkerdef.Result{Status: status, Metrics: metrics, Output: output}
}

// activeSince returns the seconds since the unit entered the active state.
// The timestamp carries the host's zone abbreviation, which time.Parse
// resolves against the local zone (the one systemctl printed it in).
func activeSince(raw string, now time.Time) (float64, bool) {
	if raw == "" || raw == "n/a" {
		return 0, false
	}

	at, err := time.ParseInLocation(timestampLayout, raw, time.Local)
	if err != nil {
		return 0, false
	}

	since := now.Sub(at).Seconds()
	if since < 0 {
		return 0, false
	}

	return since, true
}
