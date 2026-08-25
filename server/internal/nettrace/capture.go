package nettrace

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MaxCaptureBytes bounds a serialized capture. A 30-hop, 3-round trace is a
// couple of kilobytes; this is a sanity ceiling for the untrusted agent upload
// path, not a working limit.
const MaxCaptureBytes = 64 * 1024

// CaptureVersion is the schema version written into every capture.
//
// The blob is stored as an opaque attachment and read back by a dashboard that
// deploys on its own schedule, so a reader has to be able to tell "a field I do
// not know about" from "a field that means something different now".
const CaptureVersion = 1

// ErrInvalidCapture means the bytes are not a well-formed traceroute capture.
var ErrInvalidCapture = errors.New("nettrace: invalid capture")

// Capture is the JSON attachment written under
// `incidents/<uid>/traceroute` (spec 2026-08-21-10).
//
// It is deliberately self-describing: Mode and HopAddressesVisible travel WITH
// the hops, because the same hop list means different things depending on how
// it was probed, and an operator opening the raw JSON three days later has no
// other way to know.
type Capture struct {
	// Version is CaptureVersion. Written always, read defensively.
	Version int `json:"version"`
	// Mode is how the path was probed.
	Mode Mode `json:"mode"`
	// HopAddressesVisible is false when the mode cannot observe intermediate
	// hop addresses at all (ModeTCP). It is the difference between "the routers
	// did not answer" and "we could not have heard them if they had", and every
	// surface rendering the hop table must distinguish the two.
	HopAddressesVisible bool `json:"hopAddressesVisible"`
	// Host is the configured hostname of the failing check's target.
	Host string `json:"host,omitempty"`
	// Address is the IP that was traced — the one the failing probe dialed.
	Address string `json:"address"`
	// Family is `ipv4` or `ipv6`.
	Family string `json:"family"`
	// Port is the target port (ModeTCP probes it; the others record it).
	Port int `json:"port,omitempty"`
	// Region is the probing region, stamped SERVER-SIDE from the persisted
	// result row. A deported agent is never the authority on where it ran.
	Region string `json:"region,omitempty"`
	// Trigger is `incident-open` or `incident-reopen`.
	Trigger string `json:"trigger,omitempty"`
	// Rounds / MaxHops are the settings the sweep actually used.
	Rounds  int `json:"rounds"`
	MaxHops int `json:"maxHops"`
	// StartedAt is when the trace began — AFTER the failing result was
	// reported, so it is always later than the failure it explains.
	StartedAt time.Time `json:"startedAt"`
	// DurationMs is how long the whole capture took, reverse DNS included.
	DurationMs int64 `json:"durationMs"`
	// Complete reports that the target itself answered, so the path is whole.
	Complete bool `json:"complete"`
	// Truncated reports that the budget ran out before the sweep finished. A
	// truncated capture is still evidence; it just is not the whole path.
	Truncated bool `json:"truncated,omitempty"`
	// Hops are ordered by TTL, starting at 1, with no gaps.
	Hops []Hop `json:"hops"`
}

// Hop is one TTL's aggregate across all rounds.
type Hop struct {
	// TTL is the hop number, 1-based.
	TTL int `json:"ttl"`
	// Address is the first router observed at this TTL, or "" when none
	// answered (or when the mode cannot see them — check HopAddressesVisible
	// before concluding anything from an empty address).
	Address string `json:"address,omitempty"`
	// Addresses is the full distinct set when a load-balanced path answered
	// from more than one router at this TTL. Absent for the ordinary one-router
	// case; Address is always the first of them.
	Addresses []string `json:"addresses,omitempty"`
	// Hostname is the PTR of Address, when one resolved inside the budget.
	Hostname string `json:"hostname,omitempty"`
	// Sent / Received are the probe counts this TTL accumulated. They are the
	// denominator and numerator of LossPct, kept so a reader can tell 100% loss
	// over three probes from 100% loss over one.
	Sent     int `json:"sent"`
	Received int `json:"received"`
	// LossPct is 0-100, two decimals.
	LossPct float64 `json:"lossPct"`
	// RTT aggregates in milliseconds, omitted entirely when nothing answered.
	RTTMinMs float64 `json:"rttMinMs,omitempty"`
	RTTAvgMs float64 `json:"rttAvgMs,omitempty"`
	RTTMaxMs float64 `json:"rttMaxMs,omitempty"`
	// Final marks the hop where the target itself answered.
	Final bool `json:"final,omitempty"`
	// Unreachable marks a hop that answered destination-unreachable rather
	// than TTL-exceeded.
	Unreachable bool `json:"unreachable,omitempty"`
}

// Marshal renders the capture as the attachment bytes, stamping the schema
// version.
func (c *Capture) Marshal() ([]byte, error) {
	c.Version = CaptureVersion

	body, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("marshal traceroute capture: %w", err)
	}

	if len(body) > MaxCaptureBytes {
		return nil, fmt.Errorf("%w: %d bytes exceeds the cap", ErrInvalidCapture, len(body))
	}

	return body, nil
}

// ParseCapture decodes and VALIDATES attachment bytes.
//
// This is the sniff the attachment endpoint runs on an agent upload, so it is
// strict on purpose: "it is valid JSON" is not a good enough answer for bytes a
// deported agent chose. Unknown fields are rejected, which is also why Version
// exists — a newer agent's capture is refused loudly by an older server rather
// than stored as something the dashboard will render wrong.
func ParseCapture(body []byte) (*Capture, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("%w: empty", ErrInvalidCapture)
	}

	if len(body) > MaxCaptureBytes {
		return nil, fmt.Errorf("%w: %d bytes exceeds the cap", ErrInvalidCapture, len(body))
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()

	var capture Capture
	if err := decoder.Decode(&capture); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidCapture, err.Error())
	}

	if capture.Version != CaptureVersion {
		return nil, fmt.Errorf("%w: unsupported version %d", ErrInvalidCapture, capture.Version)
	}

	switch capture.Mode {
	case ModeICMPRaw, ModeICMPUDP, ModeTCP:
	default:
		return nil, fmt.Errorf("%w: unknown mode %q", ErrInvalidCapture, capture.Mode)
	}

	if capture.Address == "" {
		return nil, fmt.Errorf("%w: no address", ErrInvalidCapture)
	}

	return &capture, nil
}
