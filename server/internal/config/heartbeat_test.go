package config //nolint:testpackage // exercises unexported applyHeartbeatEnv wiring via Load

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestHeartbeatPushOffByDefault pins the shipped posture: no listener binds
// unless an operator explicitly asks for one. Enabling raw TCP/UDP ports is a
// deployment decision, never a default.
func TestHeartbeatPushOffByDefault(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg, err := Load()
	r.NoError(err)
	r.Empty(cfg.Heartbeat.TCPListen)
	r.Empty(cfg.Heartbeat.UDPListen)
	r.False(cfg.Heartbeat.TCPEnabled())
	r.False(cfg.Heartbeat.UDPEnabled())
	// The knobs that only matter once a listener is up still carry their
	// documented values, so turning one on needs exactly one variable.
	r.Equal(DefaultHeartbeatTimestampWindow, cfg.Heartbeat.ResolvedTimestampWindow())
	r.Equal(DefaultHeartbeatIdleTimeout, cfg.Heartbeat.ResolvedIdleTimeout())
	r.True(cfg.Heartbeat.UDPReplyOK)
}

// TestHeartbeatEnvVarsBind is the guard against the koanf multi-word quirk:
// every key in HeartbeatConfig contains an underscore, so the automatic env
// provider maps SP_HEARTBEAT_TCP_LISTEN onto heartbeat.tcp.listen and binds
// nothing. Without applyHeartbeatEnv these variables parse and silently do
// nothing, which is exactly the failure this test exists to catch.
func TestHeartbeatEnvVarsBind(t *testing.T) {
	t.Setenv("SP_HEARTBEAT_TCP_LISTEN", "127.0.0.1:14001")
	t.Setenv("SP_HEARTBEAT_UDP_LISTEN", "4002")
	t.Setenv("SP_HEARTBEAT_TIMESTAMP_WINDOW", "90s")
	t.Setenv("SP_HEARTBEAT_IDLE_TIMEOUT", "2m")
	t.Setenv("SP_HEARTBEAT_RATE_PER_MINUTE", "17")
	t.Setenv("SP_HEARTBEAT_RATE_BURST", "5")
	t.Setenv("SP_HEARTBEAT_MAX_SOURCE_IPS", "99")
	t.Setenv("SP_HEARTBEAT_MAX_CONNECTIONS", "13")
	t.Setenv("SP_HEARTBEAT_UDP_REPLY_OK", "false")

	r := require.New(t)

	cfg, err := Load()
	r.NoError(err)
	r.Equal("127.0.0.1:14001", cfg.Heartbeat.TCPListen)
	r.Equal(":4002", NormalizeHeartbeatListen(cfg.Heartbeat.UDPListen))
	r.Equal(90*time.Second, cfg.Heartbeat.TimestampWindow)
	r.Equal(2*time.Minute, cfg.Heartbeat.IdleTimeout)
	r.Equal(17, cfg.Heartbeat.RatePerMinute)
	r.Equal(5, cfg.Heartbeat.RateBurst)
	r.Equal(99, cfg.Heartbeat.MaxSourceIPs)
	r.Equal(13, cfg.Heartbeat.MaxConnections)
	r.False(cfg.Heartbeat.UDPReplyOK)

	// Positive control: the values above are not the defaults, so an assertion
	// cannot pass vacuously by reading an unbound struct.
	r.NotEqual(DefaultHeartbeatTimestampWindow, cfg.Heartbeat.TimestampWindow)
	r.NotEqual(DefaultHeartbeatRatePerMinute, cfg.Heartbeat.RatePerMinute)
}

// TestHeartbeatEnvVarsAreRecognized guards the startup env check: a name the
// server reads must also be reported as recognized, otherwise an operator who
// sets it correctly is told it is a typo.
func TestHeartbeatEnvVarsAreRecognized(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	recognized := make(map[string]struct{})
	for _, name := range RecognizedEnvVars() {
		recognized[name] = struct{}{}
	}

	for _, name := range []string{
		"SP_HEARTBEAT_TCP_LISTEN", "SP_HEARTBEAT_UDP_LISTEN",
		"SP_HEARTBEAT_TIMESTAMP_WINDOW", "SP_HEARTBEAT_IDLE_TIMEOUT",
		"SP_HEARTBEAT_RATE_PER_MINUTE", "SP_HEARTBEAT_RATE_BURST",
		"SP_HEARTBEAT_MAX_SOURCE_IPS", "SP_HEARTBEAT_MAX_CONNECTIONS",
		"SP_HEARTBEAT_UDP_REPLY_OK",
	} {
		r.Contains(recognized, name)
	}

	// Positive control: the name koanf's env loader WOULD have produced is not
	// recognized, which is the whole reason the manual reader exists.
	r.NotContains(recognized, "SP_HEARTBEAT_TCP")
}

func TestNormalizeHeartbeatListen(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"empty is off", "", ""},
		{"false is off", "false", ""},
		{"off is off", "OFF", ""},
		{"zero is off", "0", ""},
		{"true means the default port", "true", ":4001"},
		{"yes means the default port", "yes", ":4001"},
		{"bare port gains a colon", "4001", ":4001"},
		{"other bare port gains a colon", "9999", ":9999"},
		{"explicit address is untouched", "127.0.0.1:14001", "127.0.0.1:14001"},
		{"colon form is untouched", ":4001", ":4001"},
		{"whitespace is trimmed", "  :4001  ", ":4001"},
		{"out-of-range number is not a port", "70000", "70000"},
	} {
		r := require.New(t)
		r.Equal(tc.want, NormalizeHeartbeatListen(tc.input), tc.name)
	}
}
