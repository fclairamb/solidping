// Package aichecks turns a prompt into a `js` check script and repairs that
// script when it drifts (spec 2026-10-03-07). The LLM writes the script once;
// from then on the plain script runs with zero AI calls. A repair only ever
// targets drift (the script broke), never a real incident (the service
// broke), and is a proposal unless the check opted into `repair: auto`.
package aichecks

import (
	"strings"
	"time"

	jsconfig "github.com/fclairamb/solidping/server/internal/checkers/checkjs/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// FailureClass is how a js result failed.
type FailureClass string

// Failure classes.
const (
	// FailureNone is a passing result.
	FailureNone FailureClass = "none"
	// FailureDrift is the script breaking: an exception, or a failure the
	// script itself tagged `output.failure = "drift"`.
	FailureDrift FailureClass = "drift"
	// FailureAssertion is the service breaking: a clean `down`.
	FailureAssertion FailureClass = "assertion"
	// FailureTimeout is the runtime cutting the script off.
	FailureTimeout FailureClass = "timeout"
	// FailureConnection is the target unreachable.
	FailureConnection FailureClass = "connection"
)

// Output keys the classifier reads.
const (
	outputKeyFailure = "failure"
	outputKeyError   = "error"

	failureTagDrift     = "drift"
	failureTagAssertion = "assertion"
)

// connectionMarkers are substrings of a Go network error. A script exception
// carrying one is the target being unreachable, not the script drifting.
var connectionMarkers = []string{ //nolint:gochecknoglobals // read-only table
	"connection refused", "no such host", "dial tcp", "dial udp", "i/o timeout",
	"connection reset", "network is unreachable", "no route to host", "tls handshake",
	"context deadline exceeded", "egress",
}

// ClassifyFailure classifies a js result from its status and output.
func ClassifyFailure(status models.ResultStatus, output map[string]any) FailureClass {
	switch status {
	case models.ResultStatusUp, models.ResultStatusWarning, models.ResultStatusDegraded:
		return FailureNone
	case models.ResultStatusTimeout:
		return FailureTimeout
	case models.ResultStatusDown:
		return classifyTagged(output, FailureAssertion)
	case models.ResultStatusError:
		return classifyError(output)
	case models.ResultStatusCreated, models.ResultStatusRunning, models.ResultStatusAbandoned:
		return FailureNone
	}

	return FailureNone
}

func classifyTagged(output map[string]any, fallback FailureClass) FailureClass {
	tag, _ := output[outputKeyFailure].(string)

	switch strings.ToLower(strings.TrimSpace(tag)) {
	case failureTagDrift:
		return FailureDrift
	case failureTagAssertion:
		return FailureAssertion
	}

	return fallback
}

func classifyError(output map[string]any) FailureClass {
	if tagged := classifyTagged(output, ""); tagged != "" {
		return tagged
	}

	msg, _ := output[outputKeyError].(string)
	lower := strings.ToLower(msg)

	for _, marker := range connectionMarkers {
		if strings.Contains(lower, marker) {
			return FailureConnection
		}
	}

	// A thrown exception, a missing return, a bad status value: the script
	// itself is broken.
	return FailureDrift
}

// Repair gate defaults.
const (
	// DefaultConsecutiveFailures is how many failing runs in a row a repair
	// needs.
	DefaultConsecutiveFailures = 3
	// DefaultOrgDailyAttempts caps repair attempts per org per UTC day.
	DefaultOrgDailyAttempts = 20
	// AttemptCooldown is the per-check gap between repair attempts.
	AttemptCooldown = 24 * time.Hour
	// AutoApplyCooldown is the per-check gap between applied auto-repairs.
	AutoApplyCooldown = 24 * time.Hour
)

// Gates is everything the repair decision looks at.
type Gates struct {
	Mode jsconfig.RepairMode
	// Class is the latest result's class.
	Class FailureClass
	// ConsecutiveFailures counts the newest failing runs in a row.
	ConsecutiveFailures int
	// Threshold is the required run of failures (0 = default).
	Threshold int
	// TargetHealthy is the base URL answering 2xx/3xx.
	TargetHealthy bool
	// LastAttempt is the check's previous repair attempt, zero for none.
	LastAttempt time.Time
	// OrgAttemptsToday counts the org's attempts this UTC day.
	OrgAttemptsToday int
	// OrgDailyCap caps them (0 = default).
	OrgDailyCap int
	Now         time.Time
}

// Refusal reasons.
const (
	ReasonRepairOff     = "repair is off"
	ReasonNotDrift      = "failure is not drift"
	ReasonNotEnough     = "not enough consecutive failures"
	ReasonTargetDown    = "target does not look healthy"
	ReasonCheckCooldown = "a repair was attempted in the last 24 h"
	ReasonOrgCap        = "the organization's daily repair cap is reached"
)

// ShouldRepair reports whether a repair attempt may start, and why not.
// Every gate must be open; only drift ever qualifies.
func ShouldRepair(gates *Gates) (bool, string) {
	if gates.Mode != jsconfig.RepairPropose && gates.Mode != jsconfig.RepairAuto {
		return false, ReasonRepairOff
	}

	if gates.Class != FailureDrift {
		return false, ReasonNotDrift
	}

	threshold := gates.Threshold
	if threshold <= 0 {
		threshold = DefaultConsecutiveFailures
	}

	if gates.ConsecutiveFailures < threshold {
		return false, ReasonNotEnough
	}

	if !gates.LastAttempt.IsZero() && gates.Now.Sub(gates.LastAttempt) < AttemptCooldown {
		return false, ReasonCheckCooldown
	}

	orgCap := gates.OrgDailyCap
	if orgCap <= 0 {
		orgCap = DefaultOrgDailyAttempts
	}

	if gates.OrgAttemptsToday >= orgCap {
		return false, ReasonOrgCap
	}

	if !gates.TargetHealthy {
		return false, ReasonTargetDown
	}

	return true, ""
}

// WantsRepair reports whether a check config is an AI-authored js script
// whose repair mode is not off. Cheap: it reads the raw map.
func WantsRepair(config map[string]any) bool {
	block, ok := config["ai"].(map[string]any)
	if !ok {
		return false
	}

	mode, _ := block["repair"].(string)

	return mode != string(jsconfig.RepairOff)
}
