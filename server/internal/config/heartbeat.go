package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultHeartbeatPushPort is the port both the TCP and the UDP embedded
// heartbeat listeners bind when they are enabled without an explicit address.
//
// The SAME number on both transports on purpose: one number to document, one
// firewall rule, adjacent to the app's own 4000. The protocol version is
// carried by the SP1/SP2 line prefix, never by the port, so one port serves
// every future message form.
const DefaultHeartbeatPushPort = 4001

// Defaults for HeartbeatConfig. Exported so the listeners and their tests
// cannot drift from the documented values.
const (
	// DefaultHeartbeatTimestampWindow is how far an SP2 `ts` may sit from
	// server time. The timestamp's only job is bounding damage if the
	// server-side counter state is ever lost (a restored backup), so a window
	// wide enough to survive an unsynchronised device clock is the right
	// trade-off.
	DefaultHeartbeatTimestampWindow = 5 * time.Minute
	// DefaultHeartbeatIdleTimeout closes a TCP connection that has not sent a
	// line for this long. A device holding a socket open is NOT a heartbeat.
	DefaultHeartbeatIdleTimeout = 10 * time.Minute
	// DefaultHeartbeatRatePerMinute / DefaultHeartbeatRateBurst are the
	// per-source-IP budget applied to both listeners.
	DefaultHeartbeatRatePerMinute = 120
	DefaultHeartbeatRateBurst     = 60
	// DefaultHeartbeatMaxSourceIPs caps how many per-IP buckets are held at
	// once, so a spoofed-source flood cannot grow the map without bound.
	DefaultHeartbeatMaxSourceIPs = 10000
	// DefaultHeartbeatMaxConnections caps concurrent TCP connections.
	DefaultHeartbeatMaxConnections = 512
)

// HeartbeatConfig turns on the embedded push transports for heartbeat checks:
// two tiny listeners that accept one newline-delimited beat per line and feed
// the very same heartbeat ingest path as HTTPS.
//
// **Off by default.** Enabling is a deployment decision — the ports have to be
// exposed on the Kubernetes Service / load balancer, which this configuration
// deliberately does not automate.
//
// Every key here is multi-word, and koanf's env loader collapses every
// underscore in an SP_* name to a dot (heartbeat.tcp.listen), so NONE of them
// are reachable by the automatic env provider. They are read by hand in
// applyHeartbeatEnv and listed in manualReaderEnvVars — see
// project_koanf_env_quirk.
type HeartbeatConfig struct {
	// TCPListen is the listen address of the TCP beat listener
	// (SP_HEARTBEAT_TCP_LISTEN). Empty disables it. ":4001" is the documented
	// value; a bare port ("4001") or a plain truthy word ("true") normalizes
	// to ":4001" — see NormalizeHeartbeatListen.
	TCPListen string `koanf:"tcp_listen"`
	// UDPListen is the listen address of the UDP beat listener
	// (SP_HEARTBEAT_UDP_LISTEN). Empty disables it. Same normalization.
	UDPListen string `koanf:"udp_listen"`
	// TimestampWindow bounds how far an SP2 beat's `ts` may sit from server
	// time (SP_HEARTBEAT_TIMESTAMP_WINDOW). `ts = 0` means "no clock on this
	// device" and skips the check entirely.
	TimestampWindow time.Duration `koanf:"timestamp_window"`
	// IdleTimeout closes an idle TCP connection (SP_HEARTBEAT_IDLE_TIMEOUT).
	IdleTimeout time.Duration `koanf:"idle_timeout"`
	// RatePerMinute / RateBurst are the per-source-IP beat budget applied to
	// both listeners (SP_HEARTBEAT_RATE_PER_MINUTE / _RATE_BURST). 0 disables
	// rate limiting, which is only ever right on a closed network.
	RatePerMinute int `koanf:"rate_per_minute"`
	RateBurst     int `koanf:"rate_burst"`
	// MaxSourceIPs caps the per-IP bucket map (SP_HEARTBEAT_MAX_SOURCE_IPS).
	// UDP source addresses are spoofable, so this is a memory bound, never an
	// identity statement.
	MaxSourceIPs int `koanf:"max_source_ips"`
	// MaxConnections caps concurrent TCP connections
	// (SP_HEARTBEAT_MAX_CONNECTIONS).
	MaxConnections int `koanf:"max_connections"`
	// UDPReplyOK sends a two-byte "OK" back on an ACCEPTED datagram
	// (SP_HEARTBEAT_UDP_REPLY_OK, default true). Never more bytes than were
	// received, so the listener can never be an amplification vector, and
	// never anything at all on a failure, so it is never a validity oracle.
	UDPReplyOK bool `koanf:"udp_reply_ok"`
}

// DefaultHeartbeatConfig returns the shipped defaults: both listeners off.
func DefaultHeartbeatConfig() HeartbeatConfig {
	return HeartbeatConfig{
		TCPListen:       "",
		UDPListen:       "",
		TimestampWindow: DefaultHeartbeatTimestampWindow,
		IdleTimeout:     DefaultHeartbeatIdleTimeout,
		RatePerMinute:   DefaultHeartbeatRatePerMinute,
		RateBurst:       DefaultHeartbeatRateBurst,
		MaxSourceIPs:    DefaultHeartbeatMaxSourceIPs,
		MaxConnections:  DefaultHeartbeatMaxConnections,
		UDPReplyOK:      true,
	}
}

// NormalizeHeartbeatListen resolves a configured listen value to a concrete
// address, or "" when the listener is off.
//
//   - ""/"false"/"off"/"no"/"0"/"disabled" → "" (off)
//   - "true"/"on"/"yes"/"enabled"          → ":4001"
//   - "4001"                               → ":4001"
//   - anything else                        → unchanged
//
// The truthy words exist because "turn the listener on" is the overwhelmingly
// common operation and a Kubernetes manifest should not have to repeat the
// port number it already declares in the Service.
func NormalizeHeartbeatListen(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}

	switch strings.ToLower(trimmed) {
	case "false", "off", "no", "0", "disabled":
		return ""
	case "true", "on", "yes", "enabled":
		return ":" + strconv.Itoa(DefaultHeartbeatPushPort)
	}

	if port, err := strconv.Atoi(trimmed); err == nil && port > 0 && port <= 65535 {
		return ":" + trimmed
	}

	return trimmed
}

// TCPEnabled reports whether the TCP beat listener should be started.
func (c *HeartbeatConfig) TCPEnabled() bool {
	return NormalizeHeartbeatListen(c.TCPListen) != ""
}

// UDPEnabled reports whether the UDP beat listener should be started.
func (c *HeartbeatConfig) UDPEnabled() bool {
	return NormalizeHeartbeatListen(c.UDPListen) != ""
}

// ResolvedTimestampWindow returns the SP2 timestamp tolerance, falling back to
// the default when unset or non-positive.
func (c *HeartbeatConfig) ResolvedTimestampWindow() time.Duration {
	if c.TimestampWindow <= 0 {
		return DefaultHeartbeatTimestampWindow
	}

	return c.TimestampWindow
}

// ResolvedIdleTimeout returns the TCP idle timeout, falling back to the
// default when unset or non-positive.
func (c *HeartbeatConfig) ResolvedIdleTimeout() time.Duration {
	if c.IdleTimeout <= 0 {
		return DefaultHeartbeatIdleTimeout
	}

	return c.IdleTimeout
}

// ResolvedMaxConnections returns the TCP connection cap, falling back to the
// default when unset or non-positive.
func (c *HeartbeatConfig) ResolvedMaxConnections() int {
	if c.MaxConnections <= 0 {
		return DefaultHeartbeatMaxConnections
	}

	return c.MaxConnections
}

// ResolvedMaxSourceIPs returns the per-IP bucket cap, falling back to the
// default when unset or non-positive.
func (c *HeartbeatConfig) ResolvedMaxSourceIPs() int {
	if c.MaxSourceIPs <= 0 {
		return DefaultHeartbeatMaxSourceIPs
	}

	return c.MaxSourceIPs
}

// applyHeartbeatEnv reads the SP_HEARTBEAT_* names by hand. Every koanf key in
// HeartbeatConfig has an underscore, so the automatic env provider would map
// SP_HEARTBEAT_TCP_LISTEN onto heartbeat.tcp.listen and bind nothing at all —
// the variable would parse and then silently do nothing. Same quirk as
// rate_limiting / acme.
func applyHeartbeatEnv(cfg *HeartbeatConfig) {
	strEnv := func(name string, dst *string) {
		if v := os.Getenv(name); v != "" {
			*dst = v
		}
	}
	intEnv := func(name string, dst *int) {
		v := os.Getenv(name)
		if v == "" {
			return
		}

		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
	}
	durEnv := func(name string, dst *time.Duration) {
		v := os.Getenv(name)
		if v == "" {
			return
		}

		if d, err := time.ParseDuration(v); err == nil {
			*dst = d
		}
	}

	strEnv("SP_HEARTBEAT_TCP_LISTEN", &cfg.TCPListen)
	strEnv("SP_HEARTBEAT_UDP_LISTEN", &cfg.UDPListen)
	durEnv("SP_HEARTBEAT_TIMESTAMP_WINDOW", &cfg.TimestampWindow)
	durEnv("SP_HEARTBEAT_IDLE_TIMEOUT", &cfg.IdleTimeout)
	intEnv("SP_HEARTBEAT_RATE_PER_MINUTE", &cfg.RatePerMinute)
	intEnv("SP_HEARTBEAT_RATE_BURST", &cfg.RateBurst)
	intEnv("SP_HEARTBEAT_MAX_SOURCE_IPS", &cfg.MaxSourceIPs)
	intEnv("SP_HEARTBEAT_MAX_CONNECTIONS", &cfg.MaxConnections)

	if v := os.Getenv("SP_HEARTBEAT_UDP_REPLY_OK"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			cfg.UDPReplyOK = b
		}
	}
}
