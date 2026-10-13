package checkicmp

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

var errTestTimeout = errors.New("timeout")

func fptr(v float64) *float64 { return &v }

// burst builds results with `ok` replies of the given RTT out of `total`.
func burst(total, ok int, rtt time.Duration) []pingResult {
	out := make([]pingResult, 0, total)

	for idx := range total {
		if idx < ok {
			out = append(out, pingResult{Success: true, RTT: rtt})
		} else {
			out = append(out, pingResult{Error: errTestTimeout})
		}
	}

	return out
}

func TestSummarizeBurst_Thresholds(t *testing.T) {
	t.Parallel()

	ms := time.Millisecond
	tests := []struct {
		name       string
		cfg        ICMPConfig
		results    []pingResult
		wantStatus checkerdef.Status
		wantErr    string
	}{
		{"no thresholds, 1 of 5 up", ICMPConfig{}, burst(5, 1, 10*ms), checkerdef.StatusUp, ""},
		{
			"loss 40 over 20",
			ICMPConfig{MaxPacketLossPercent: fptr(20)},
			burst(5, 3, 10*ms),
			checkerdef.StatusDown, "packet loss 40% > 20%",
		},
		{"loss 20 at 20 is up", ICMPConfig{MaxPacketLossPercent: fptr(20)}, burst(5, 4, 10*ms), checkerdef.StatusUp, ""},
		{"loss 0 max 0 up", ICMPConfig{MaxPacketLossPercent: fptr(0)}, burst(10, 10, 10*ms), checkerdef.StatusUp, ""},
		{
			"1 of 10 lost max 0",
			ICMPConfig{MaxPacketLossPercent: fptr(0)},
			burst(10, 9, 10*ms),
			checkerdef.StatusDown, "packet loss 10% > 0%",
		},
		{
			"fractional loss",
			ICMPConfig{MaxPacketLossPercent: fptr(10)},
			burst(3, 2, 10*ms),
			checkerdef.StatusDown, "packet loss 33.3% > 10%",
		},
		{
			"avg 180 over 100",
			ICMPConfig{MaxAvgRTTMs: fptr(100)},
			burst(3, 3, 180*ms),
			checkerdef.StatusDown, "avg rtt 180 ms > 100 ms",
		},
		{"avg 90 under 100", ICMPConfig{MaxAvgRTTMs: fptr(100)}, burst(3, 3, 90*ms), checkerdef.StatusUp, ""},
		{
			"both breached",
			ICMPConfig{MaxPacketLossPercent: fptr(20), MaxAvgRTTMs: fptr(100)},
			burst(5, 3, 180*ms),
			checkerdef.StatusDown, "packet loss 40% > 20%; avg rtt 180 ms > 100 ms",
		},
		{
			"total loss keeps its message",
			ICMPConfig{MaxPacketLossPercent: fptr(20), MaxAvgRTTMs: fptr(100)},
			[]pingResult{{}, {}},
			checkerdef.StatusDown, "no successful ping responses",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := tt.cfg
			cfg.Host = "example.com"
			res := summarizeBurst(&cfg, net.IPv4(192, 0, 2, 1), tt.results, time.Second)

			if res.Status != tt.wantStatus {
				t.Fatalf("status = %v, want %v (output %v)", res.Status, tt.wantStatus, res.Output)
			}

			got, _ := res.Output[checkerdef.OutputKeyError].(string)
			if got != tt.wantErr {
				t.Errorf("error = %q, want %q", got, tt.wantErr)
			}

			hasNF := res.Diagnostics != nil && res.Diagnostics.NetworkFailure != nil
			wantNF := tt.name == "total loss keeps its message"
			if hasNF != wantNF {
				t.Errorf("network failure set = %v, want %v", hasNF, wantNF)
			}
		})
	}
}

func TestICMPConfig_Thresholds(t *testing.T) {
	t.Parallel()

	var cfg ICMPConfig
	if err := cfg.FromMap(map[string]any{"host": "h", "max_packet_loss_percent": 0, "max_avg_rtt_ms": 150.5}); err != nil {
		t.Fatal(err)
	}

	if cfg.MaxPacketLossPercent == nil || *cfg.MaxPacketLossPercent != 0 || *cfg.MaxAvgRTTMs != 150.5 {
		t.Fatalf("bad parse: %+v", cfg)
	}

	out := cfg.GetConfig()
	if v, ok := out["max_packet_loss_percent"]; !ok || v != 0.0 {
		t.Errorf("loss 0 must round-trip, got %v", out)
	}

	if out["max_avg_rtt_ms"] != 150.5 {
		t.Errorf("rtt round-trip: %v", out)
	}

	var empty ICMPConfig
	if err := empty.FromMap(map[string]any{"host": "h"}); err != nil {
		t.Fatal(err)
	}

	if _, ok := empty.GetConfig()["max_packet_loss_percent"]; ok {
		t.Error("unset must be absent")
	}

	for _, key := range []string{"max_packet_loss_percent", "max_avg_rtt_ms"} {
		if err := (&ICMPConfig{}).FromMap(map[string]any{"host": "h", key: "5"}); err == nil {
			t.Errorf("%s string must be rejected", key)
		}
	}
}

func TestICMPChecker_ValidateThresholds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key     string
		val     float64
		wantErr bool
	}{
		{"max_packet_loss_percent", 0, false},
		{"max_packet_loss_percent", 100, false},
		{"max_packet_loss_percent", -1, true},
		{"max_packet_loss_percent", 101, true},
		{"max_avg_rtt_ms", 1, false},
		{"max_avg_rtt_ms", 30000, false},
		{"max_avg_rtt_ms", 0, true},
		{"max_avg_rtt_ms", -5, true},
		{"max_avg_rtt_ms", 30001, true},
	}

	for _, tt := range tests {
		err := (&ICMPChecker{}).Validate(&checkerdef.CheckSpec{Config: map[string]any{"host": "example.com", tt.key: tt.val}})
		if (err != nil) != tt.wantErr {
			t.Errorf("%s=%v: err=%v, wantErr=%v", tt.key, tt.val, err, tt.wantErr)
		}
	}
}
