// Package freebox provides a thin HTTP client and service helpers for
// talking to the local Freebox OS API (the router operating system shipped
// by the French ISP Free on every Freebox device).
//
// The API is served at /api/v4/ on the LAN (http://mafreebox.freebox.fr)
// or, when the operator has opted-in, on a custom HTTPS hostname for
// remote access. Authentication is challenge-based and requires a one-time
// LCD-approval step on the Freebox itself — see the project spec
// `2026-05-24-06-freebox-os-integration.md` for the full lifecycle.
//
// This foundation package only covers the connection + pairing flow.
// Higher-level checkers (line quality, LAN discovery, …) live in their
// own packages and consume the client defined here.
package freebox

import "encoding/json"

// Default values for the SolidPing app identity on the Freebox admin.
const (
	DefaultAppID      = "io.solidping"
	DefaultAppName    = "SolidPing"
	DefaultAppVersion = "1.0.0"
	DefaultDeviceName = "SolidPing"
	DefaultBaseURL    = "http://mafreebox.freebox.fr"
)

// Pairing-status string values returned by /login/authorize/{trackID}.
//
// The Freebox uses lowercase string enums; we mirror them verbatim here so
// callers can round-trip the value through JSON and DB columns without
// any case-folding surprises.
const (
	StatusUnknown = "unknown"
	StatusPending = "pending"
	StatusGranted = "granted"
	StatusDenied  = "denied"
	StatusTimeout = "timeout"
)

// AuthorizeRequest is the body for POST /api/v4/login/authorize/. The
// Freebox shows app_name + device_name on the LCD pairing prompt, so we
// expose them as inputs the operator can tweak from the UI.
//
//nolint:tagliatelle // JSON tags must match Freebox API field names
type AuthorizeRequest struct {
	AppID      string `json:"app_id"`
	AppName    string `json:"app_name"`
	AppVersion string `json:"app_version"`
	DeviceName string `json:"device_name"`
}

// AuthorizeResult is the result payload returned by the authorize call.
// The app_token is permanent (survives reboots) and must be persisted
// immediately, encrypted; track_id is short-lived and used only to poll
// the pairing status.
//
//nolint:tagliatelle // JSON tags must match Freebox API field names
type AuthorizeResult struct {
	AppToken string `json:"app_token"`
	TrackID  int    `json:"track_id"`
}

// PairingStatus is the body of GET /api/v4/login/authorize/{trackID}.
type PairingStatus struct {
	Status string `json:"status"`
}

// LoginChallenge is the body of GET /api/v4/login/.
type LoginChallenge struct {
	Challenge string `json:"challenge"`
}

// SessionRequest is the body for POST /api/v4/login/session/.
//
//nolint:tagliatelle // JSON tags must match Freebox API field names
type SessionRequest struct {
	AppID    string `json:"app_id"`
	Password string `json:"password"`
}

// SessionResult is the result payload from the session call. We only
// keep the session_token; the permission set is informational.
//
//nolint:tagliatelle // JSON tags must match Freebox API field names
type SessionResult struct {
	SessionToken string         `json:"session_token"`
	Permissions  map[string]any `json:"permissions"`
}

// APIResponse is the generic envelope every Freebox /api/v4/ endpoint
// returns. When Success is false, ErrorCode is set to a machine-readable
// constant ("auth_required", "invalid_token", …) and Msg holds the
// human-readable French/English message.
type APIResponse struct {
	Success   bool   `json:"success"`
	Msg       string `json:"msg,omitempty"`
	ErrorCode string `json:"error_code,omitempty"`
	UID       string `json:"uid,omitempty"`
	// Result is left as raw JSON so the caller can decode it into the
	// appropriate type — Freebox endpoints return wildly different
	// shapes here.
	Result json.RawMessage `json:"result,omitempty"`
}
