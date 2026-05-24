package checkfreeboxline_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkfreeboxline"
	"github.com/fclairamb/solidping/server/internal/integrations/freebox"
)

// fakeFreebox is a small mock of the relevant /api/v4/ surface for the
// freebox_line checker — only `/login/`, `/login/session/`,
// `/connection/`, `/connection/xdsl/` and `/connection/ftth/` are
// exercised. Behavior is configurable per test via the response fields,
// which are read by the handler closures.
type fakeFreebox struct {
	t              *testing.T
	appToken       string
	challenge      string
	session        string
	wanResp        any
	xdslResp       any
	ftthResp       any
	wanErrorCode   string // when non-empty, /connection/ returns this error envelope
	xdslErrorCode  string
	ftthErrorCode  string
	loginHits      atomic.Int32
	sessionHits    atomic.Int32
	connHits       atomic.Int32
	xdslHits       atomic.Int32
	ftthHits       atomic.Int32
	failFirstAuth  bool // simulate one auth_required + force a session refresh
	sessionRefresh atomic.Bool
}

func (f *fakeFreebox) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/v4/login/", func(w http.ResponseWriter, _ *http.Request) {
		f.loginHits.Add(1)
		writeOK(f.t, w, freebox.LoginChallenge{Challenge: f.challenge})
	})

	mux.HandleFunc("/api/v4/login/session/", func(w http.ResponseWriter, r *http.Request) {
		f.sessionHits.Add(1)

		var body freebox.SessionRequest

		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)

			return
		}

		want := expectedHMAC(f.appToken, f.challenge)
		if body.Password != want {
			writeErr(f.t, w, "invalid_token", "bad password")

			return
		}

		writeOK(f.t, w, freebox.SessionResult{SessionToken: f.session})
	})

	mux.HandleFunc("/api/v4/connection/", func(w http.ResponseWriter, r *http.Request) {
		f.connHits.Add(1)

		// /connection/xdsl/ and /connection/ftth/ also match this prefix —
		// route them explicitly to keep the mux predictable.
		switch r.URL.Path {
		case "/api/v4/connection/xdsl/":
			f.serveXDSL(w, r)

			return
		case "/api/v4/connection/ftth/":
			f.serveFTTH(w, r)

			return
		}

		if !f.checkAuth(w, r) {
			return
		}

		if f.wanErrorCode != "" {
			writeErr(f.t, w, f.wanErrorCode, "")

			return
		}

		writeOK(f.t, w, f.wanResp)
	})

	return mux
}

func (f *fakeFreebox) serveXDSL(w http.ResponseWriter, r *http.Request) {
	f.xdslHits.Add(1)

	if !f.checkAuth(w, r) {
		return
	}

	if f.xdslErrorCode != "" {
		writeErr(f.t, w, f.xdslErrorCode, "")

		return
	}

	writeOK(f.t, w, f.xdslResp)
}

func (f *fakeFreebox) serveFTTH(w http.ResponseWriter, r *http.Request) {
	f.ftthHits.Add(1)

	if !f.checkAuth(w, r) {
		return
	}

	if f.ftthErrorCode != "" {
		writeErr(f.t, w, f.ftthErrorCode, "")

		return
	}

	writeOK(f.t, w, f.ftthResp)
}

// checkAuth simulates the Freebox's session check. When failFirstAuth is
// true, the first authenticated call returns auth_required to force the
// client into a session refresh; subsequent calls succeed.
func (f *fakeFreebox) checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if f.failFirstAuth && !f.sessionRefresh.Load() {
		f.sessionRefresh.Store(true)
		writeErr(f.t, w, "auth_required", "expired session")

		return false
	}

	if r.Header.Get("X-Fbx-App-Auth") != f.session {
		writeErr(f.t, w, "auth_required", "invalid session token")

		return false
	}

	return true
}

func writeOK(t *testing.T, w http.ResponseWriter, result any) {
	t.Helper()

	raw, err := json.Marshal(result)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	out := freebox.APIResponse{Success: true, Result: raw}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(out); err != nil {
		t.Logf("encode envelope: %v", err)
	}
}

func writeErr(t *testing.T, w http.ResponseWriter, code, msg string) {
	t.Helper()

	out := freebox.APIResponse{Success: false, ErrorCode: code, Msg: msg}

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(out); err != nil {
		t.Logf("encode envelope: %v", err)
	}
}

func expectedHMAC(token, challenge string) string {
	mac := hmac.New(sha1.New, []byte(token))
	mac.Write([]byte(challenge))

	return hex.EncodeToString(mac.Sum(nil))
}

// ctxWithResolver returns a context carrying the given resolver — safe
// to use from parallel tests because the resolver is request-scoped
// rather than process-global.
func ctxWithResolver(fn checkfreeboxline.ConnectionResolver) context.Context {
	return checkfreeboxline.WithResolver(context.Background(), fn)
}

// startFakeFreebox spins up an httptest server bound to fb's handler and
// registers a t.Cleanup to close it. Returns the base URL.
func startFakeFreebox(t *testing.T, fb *fakeFreebox) string {
	t.Helper()

	srv := httptest.NewServer(fb.handler())
	t.Cleanup(srv.Close)

	return srv.URL
}

func resolverFor(baseURL, appToken string) checkfreeboxline.ConnectionResolver {
	return func(_ context.Context, _ string) (*checkfreeboxline.ResolvedConnection, error) {
		return &checkfreeboxline.ResolvedConnection{
			BaseURL:  baseURL,
			AppID:    freebox.DefaultAppID,
			AppToken: appToken,
		}, nil
	}
}

func newXDSLResponse(status string, downRate, downSNR, downAttn int, downCRC int64) map[string]any {
	return map[string]any{
		"status": map[string]any{
			"status":     status,
			"modulation": "vdsl2",
			"uptime":     12345,
		},
		"down": map[string]any{
			"maxrate": 100000,
			"rate":    downRate,
			"snr":     downSNR,
			"attn":    downAttn,
			"fec":     0,
			"crc":     downCRC,
			"hec":     0,
			"es":      0,
			"ses":     0,
		},
		"up": map[string]any{
			"maxrate": 50000,
			"rate":    40000,
			"snr":     90,
			"attn":    240,
			"fec":     0,
			"crc":     0,
			"hec":     0,
			"es":      0,
			"ses":     0,
		},
	}
}

func newFTTHResponse(link, hasSignal bool, pwrRx, pwrTx int) map[string]any {
	return map[string]any{
		"sfp_present":          true,
		"sfp_alim_ok":          true,
		"sfp_has_power_report": true,
		"sfp_has_signal":       hasSignal,
		"link":                 link,
		"sfp_serial":           "SFP123",
		"sfp_vendor":           "Cisco",
		"sfp_model":            "SFP-GE-ZX",
		"sfp_pwr_tx":           pwrTx,
		"sfp_pwr_rx":           pwrRx,
	}
}

func runChecker(ctx context.Context, t *testing.T, cfg *checkfreeboxline.FreeboxLineConfig) *checkerdef.Result {
	t.Helper()

	r := require.New(t)
	checker := &checkfreeboxline.FreeboxLineChecker{}

	result, err := checker.Execute(ctx, cfg)
	r.NoError(err)
	r.NotNil(result)

	return result
}

func TestChecker_Type(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	c := &checkfreeboxline.FreeboxLineChecker{}
	r.Equal(checkerdef.CheckTypeFreeboxLine, c.Type())
}

func TestValidateConfig(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		cfg       *checkfreeboxline.FreeboxLineConfig
		wantErr   bool
		errSubstr string
	}{
		{
			name: "valid xdsl",
			cfg: &checkfreeboxline.FreeboxLineConfig{
				ConnectionUID: "conn-1", LinkType: "xdsl",
			},
		},
		{
			name: "valid ftth",
			cfg: &checkfreeboxline.FreeboxLineConfig{
				ConnectionUID: "conn-1", LinkType: "ftth",
			},
		},
		{
			name:      "missing connection uid",
			cfg:       &checkfreeboxline.FreeboxLineConfig{LinkType: "xdsl"},
			wantErr:   true,
			errSubstr: "connectionUid",
		},
		{
			name: "invalid link type",
			cfg: &checkfreeboxline.FreeboxLineConfig{
				ConnectionUID: "conn-1", LinkType: "fiber",
			},
			wantErr:   true,
			errSubstr: "linkType",
		},
		{
			name: "negative threshold",
			cfg: &checkfreeboxline.FreeboxLineConfig{
				ConnectionUID: "conn-1", LinkType: "xdsl", MinSyncRateDownKbps: -1,
			},
			wantErr:   true,
			errSubstr: "minSyncRateDownKbps",
		},
		{
			name: "min above max rx power",
			cfg: &checkfreeboxline.FreeboxLineConfig{
				ConnectionUID: "conn-1", LinkType: "ftth",
				MinRxPowerMw: 1.0, MaxRxPowerMw: 0.5,
			},
			wantErr:   true,
			errSubstr: "minRxPowerMw",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			err := tc.cfg.Validate()

			if tc.wantErr {
				r.Error(err)
				r.Contains(err.Error(), tc.errSubstr)

				return
			}

			r.NoError(err)
		})
	}
}

func TestChecker_Execute_NoResolver(t *testing.T) {
	t.Parallel()

	// Don't carry any resolver in the context; the package-level
	// resolver is also unset in tests. Execute should refuse.
	checker := &checkfreeboxline.FreeboxLineChecker{}
	cfg := &checkfreeboxline.FreeboxLineConfig{
		ConnectionUID: "conn-1", LinkType: "xdsl",
	}

	_, err := checker.Execute(context.Background(), cfg)
	require.New(t).ErrorIs(err, checkfreeboxline.ErrResolverNotConfigured)
}

func TestChecker_Execute_ResolverError(t *testing.T) {
	t.Parallel()

	ctx := ctxWithResolver(func(_ context.Context, _ string) (*checkfreeboxline.ResolvedConnection, error) {
		return nil, checkfreeboxline.ErrConnectionNotFound
	})

	cfg := &checkfreeboxline.FreeboxLineConfig{
		ConnectionUID: "missing", LinkType: "xdsl",
	}
	result := runChecker(ctx, t, cfg)
	require.New(t).Equal(checkerdef.StatusError, result.Status)
}

func TestChecker_Execute_WANDown(t *testing.T) {
	t.Parallel()

	fb := &fakeFreebox{
		t:         t,
		appToken:  "token-1",
		challenge: "ch-1",
		session:   "sess-1",
		wanResp:   map[string]any{"state": "down", "type": "xdsl"},
	}
	url := startFakeFreebox(t, fb)
	ctx := ctxWithResolver(resolverFor(url, fb.appToken))

	cfg := &checkfreeboxline.FreeboxLineConfig{
		ConnectionUID: "conn-1", LinkType: "xdsl",
	}
	result := runChecker(ctx, t, cfg)

	r := require.New(t)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.EqualValues(1, fb.connHits.Load())
	r.EqualValues(0, fb.xdslHits.Load(), "must not call /xdsl/ when WAN is down")
	r.Equal("down", result.Output[checkfreeboxline.OutputKeyState])
}

func TestChecker_Execute_XDSLUp(t *testing.T) {
	t.Parallel()

	fb := &fakeFreebox{
		t:         t,
		appToken:  "token-2",
		challenge: "ch-2",
		session:   "sess-2",
		wanResp:   map[string]any{"state": "up", "type": "xdsl"},
		// 80 Mbps sync rate, 10 dB SNR, 25 dB attenuation, no CRC errors.
		xdslResp: newXDSLResponse("showtime", 80000, 100, 250, 0),
	}
	url := startFakeFreebox(t, fb)
	ctx := ctxWithResolver(resolverFor(url, fb.appToken))

	cfg := &checkfreeboxline.FreeboxLineConfig{
		ConnectionUID:       "conn-1",
		LinkType:            "xdsl",
		MinSyncRateDownKbps: 60000,
		MinSnrMarginDownDb:  6,
		MaxAttenuationDb:    30,
		MaxCrcErrorsPerRun:  10,
	}
	result := runChecker(ctx, t, cfg)

	r := require.New(t)
	r.Equal(checkerdef.StatusUp, result.Status)
	r.Equal("xdsl", result.Output[checkfreeboxline.OutputKeyType])
	r.EqualValues(80000, result.Output[checkfreeboxline.OutputKeySyncRateDown])
	r.InDelta(10.0, result.Output[checkfreeboxline.OutputKeySnrMarginDown], 0.001)
	r.InDelta(25.0, result.Output[checkfreeboxline.OutputKeyAttenuationDown], 0.001)
	r.EqualValues(1, fb.xdslHits.Load())
}

func TestChecker_Execute_XDSLDegradedLowSnr(t *testing.T) {
	t.Parallel()

	fb := &fakeFreebox{
		t:         t,
		appToken:  "token-3",
		challenge: "ch-3",
		session:   "sess-3",
		wanResp:   map[string]any{"state": "up", "type": "xdsl"},
		// 4 dB SNR (40 / 10) — below the 6 dB threshold below.
		xdslResp: newXDSLResponse("showtime", 80000, 40, 250, 0),
	}
	url := startFakeFreebox(t, fb)
	ctx := ctxWithResolver(resolverFor(url, fb.appToken))

	cfg := &checkfreeboxline.FreeboxLineConfig{
		ConnectionUID:      "conn-1",
		LinkType:           "xdsl",
		MinSnrMarginDownDb: 6,
	}
	result := runChecker(ctx, t, cfg)

	r := require.New(t)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal(true, result.Output[checkfreeboxline.OutputKeyDegraded])
	r.Contains(result.Output[checkfreeboxline.OutputKeyDegradedReason], "SNR margin")
}

func TestChecker_Execute_XDSLDownNotShowtime(t *testing.T) {
	t.Parallel()

	fb := &fakeFreebox{
		t:         t,
		appToken:  "token-4",
		challenge: "ch-4",
		session:   "sess-4",
		wanResp:   map[string]any{"state": "up", "type": "xdsl"},
		xdslResp:  newXDSLResponse("training", 80000, 100, 250, 0),
	}
	url := startFakeFreebox(t, fb)
	ctx := ctxWithResolver(resolverFor(url, fb.appToken))

	cfg := &checkfreeboxline.FreeboxLineConfig{
		ConnectionUID: "conn-1", LinkType: "xdsl",
	}
	result := runChecker(ctx, t, cfg)

	r := require.New(t)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal("training", result.Output[checkfreeboxline.OutputKeyXDSLStatus])
}

func TestChecker_Execute_XDSLCRCErrorsAboveThreshold(t *testing.T) {
	t.Parallel()

	fb := &fakeFreebox{
		t:         t,
		appToken:  "token-crc",
		challenge: "ch-crc",
		session:   "sess-crc",
		wanResp:   map[string]any{"state": "up", "type": "xdsl"},
		xdslResp:  newXDSLResponse("showtime", 80000, 100, 250, 250),
	}
	url := startFakeFreebox(t, fb)
	ctx := ctxWithResolver(resolverFor(url, fb.appToken))

	cfg := &checkfreeboxline.FreeboxLineConfig{
		ConnectionUID:      "conn-1",
		LinkType:           "xdsl",
		MaxCrcErrorsPerRun: 100,
	}
	result := runChecker(ctx, t, cfg)

	r := require.New(t)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Contains(result.Output[checkfreeboxline.OutputKeyDegradedReason], "CRC")
}

func TestChecker_Execute_XDSLLowSyncRate(t *testing.T) {
	t.Parallel()

	fb := &fakeFreebox{
		t:         t,
		appToken:  "token-low-rate",
		challenge: "ch-low-rate",
		session:   "sess-low-rate",
		wanResp:   map[string]any{"state": "up", "type": "xdsl"},
		// Only 1 Mbps — well below the 5 Mbps floor configured below.
		xdslResp: newXDSLResponse("showtime", 1000, 100, 250, 0),
	}
	url := startFakeFreebox(t, fb)
	ctx := ctxWithResolver(resolverFor(url, fb.appToken))

	cfg := &checkfreeboxline.FreeboxLineConfig{
		ConnectionUID:       "conn-1",
		LinkType:            "xdsl",
		MinSyncRateDownKbps: 5000,
	}
	result := runChecker(ctx, t, cfg)

	r := require.New(t)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Contains(result.Output[checkfreeboxline.OutputKeyDegradedReason], "sync rate")
}

func TestChecker_Execute_XDSLHighAttenuation(t *testing.T) {
	t.Parallel()

	fb := &fakeFreebox{
		t:         t,
		appToken:  "token-attn",
		challenge: "ch-attn",
		session:   "sess-attn",
		wanResp:   map[string]any{"state": "up", "type": "xdsl"},
		// 60 dB attenuation (tenths of a dB) — above the 40 dB ceiling.
		xdslResp: newXDSLResponse("showtime", 80000, 100, 600, 0),
	}
	url := startFakeFreebox(t, fb)
	ctx := ctxWithResolver(resolverFor(url, fb.appToken))

	cfg := &checkfreeboxline.FreeboxLineConfig{
		ConnectionUID:    "conn-1",
		LinkType:         "xdsl",
		MaxAttenuationDb: 40,
	}
	result := runChecker(ctx, t, cfg)

	r := require.New(t)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Contains(result.Output[checkfreeboxline.OutputKeyDegradedReason], "attenuation")
}

func TestChecker_Execute_FTTHUp(t *testing.T) {
	t.Parallel()

	fb := &fakeFreebox{
		t:         t,
		appToken:  "token-ftth",
		challenge: "ch-ftth",
		session:   "sess-ftth",
		wanResp:   map[string]any{"state": "up", "type": "ftth"},
		// rx = -10 dBm × 100 = -1000 → 0.1 mW; tx = +2 dBm × 100 = 200 → ~1.58 mW.
		ftthResp: newFTTHResponse(true, true, -1000, 200),
	}
	url := startFakeFreebox(t, fb)
	ctx := ctxWithResolver(resolverFor(url, fb.appToken))

	cfg := &checkfreeboxline.FreeboxLineConfig{
		ConnectionUID: "conn-1",
		LinkType:      "ftth",
		MinRxPowerMw:  0.05,
		MaxRxPowerMw:  0.5,
	}
	result := runChecker(ctx, t, cfg)

	r := require.New(t)
	r.Equal(checkerdef.StatusUp, result.Status)
	r.InDelta(0.1, result.Output[checkfreeboxline.OutputKeyRxPowerMw], 0.001)
	r.InDelta(-10.0, result.Output[checkfreeboxline.OutputKeyRxPowerDbm], 0.001)
	r.Equal("Cisco", result.Output[checkfreeboxline.OutputKeySfpVendor])
	r.EqualValues(1, fb.ftthHits.Load())
}

func TestChecker_Execute_FTTHDownNoSignal(t *testing.T) {
	t.Parallel()

	fb := &fakeFreebox{
		t:         t,
		appToken:  "token-ftth-down",
		challenge: "ch-ftth-down",
		session:   "sess-ftth-down",
		wanResp:   map[string]any{"state": "up", "type": "ftth"},
		ftthResp:  newFTTHResponse(true, false, -5000, 200),
	}
	url := startFakeFreebox(t, fb)
	ctx := ctxWithResolver(resolverFor(url, fb.appToken))

	cfg := &checkfreeboxline.FreeboxLineConfig{
		ConnectionUID: "conn-1", LinkType: "ftth",
	}
	result := runChecker(ctx, t, cfg)

	r := require.New(t)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal(false, result.Output[checkfreeboxline.OutputKeySfpHasSignal])
}

func TestChecker_Execute_FTTHDegradedLowRxPower(t *testing.T) {
	t.Parallel()

	fb := &fakeFreebox{
		t:         t,
		appToken:  "token-ftth-low",
		challenge: "ch-ftth-low",
		session:   "sess-ftth-low",
		wanResp:   map[string]any{"state": "up", "type": "ftth"},
		// −30 dBm × 100 = −3000 → ~0.001 mW (below the 0.05 mW floor).
		ftthResp: newFTTHResponse(true, true, -3000, 200),
	}
	url := startFakeFreebox(t, fb)
	ctx := ctxWithResolver(resolverFor(url, fb.appToken))

	cfg := &checkfreeboxline.FreeboxLineConfig{
		ConnectionUID: "conn-1", LinkType: "ftth",
		MinRxPowerMw: 0.05,
	}
	result := runChecker(ctx, t, cfg)

	r := require.New(t)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal(true, result.Output[checkfreeboxline.OutputKeyDegraded])
	r.Contains(result.Output[checkfreeboxline.OutputKeyDegradedReason], "Rx power")
}

func TestChecker_Execute_SessionRefresh(t *testing.T) {
	t.Parallel()

	fb := &fakeFreebox{
		t:             t,
		appToken:      "token-refresh",
		challenge:     "ch-refresh",
		session:       "sess-refresh",
		wanResp:       map[string]any{"state": "up", "type": "xdsl"},
		xdslResp:      newXDSLResponse("showtime", 80000, 100, 250, 0),
		failFirstAuth: true,
	}
	url := startFakeFreebox(t, fb)
	ctx := ctxWithResolver(resolverFor(url, fb.appToken))

	cfg := &checkfreeboxline.FreeboxLineConfig{
		ConnectionUID: "conn-1", LinkType: "xdsl",
	}
	result := runChecker(ctx, t, cfg)

	r := require.New(t)
	r.Equal(checkerdef.StatusUp, result.Status)
	// The freebox client retries once after auth_required, so the
	// challenge endpoint should have been hit at least twice.
	r.GreaterOrEqual(int(fb.loginHits.Load()), 2)
}

func TestSamples_ReturnsValidConfigs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	c := &checkfreeboxline.FreeboxLineChecker{}
	samples := c.GetSampleConfigs(nil)
	r.NotEmpty(samples)

	for _, s := range samples {
		cfg := &checkfreeboxline.FreeboxLineConfig{}
		r.NoError(cfg.FromMap(s.Config))
		// connectionUid is intentionally blank in samples — the dashboard
		// fills it in — so we don't call cfg.Validate() here. Round-trip
		// just confirms the map shape stays parseable.
	}
}
