package checkfreeboxline

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/integrations/freebox"
)

// ResolvedConnection bundles the data the checker needs to talk to a
// Freebox: the base URL, the app_id used at pairing time, and the
// permanent app_token decrypted out of settings_private. The app_id
// matters because the Freebox session-open call signs the challenge with
// the same app_id used at authorize time — using the default here for a
// non-default-id pairing would be rejected as auth_required.
type ResolvedConnection struct {
	BaseURL  string
	AppID    string
	AppToken string
}

// ConnectionResolver fetches the Freebox connection details for a given
// IntegrationConnection UID. Implementations own the DB lookup and the
// credentials.Service call needed to decrypt the app_token; the checker
// stays free of those concerns to keep this package importable from
// tests without a database. The resolver is wired by the API server at
// startup (see internal/app/server.go).
type ConnectionResolver func(ctx context.Context, connectionUID string) (*ResolvedConnection, error)

// ConnectionResolverFunc is the active resolver. Production code wires
// it once at startup; tests should prefer WithResolver(ctx, …) so
// parallel tests don't race over a single global. It is intentionally a
// package-level variable rather than a checker-field so the
// registry-built checker (zero-valued struct) still has access without
// plumbing services through the checkerdef interface.
//
//nolint:gochecknoglobals // Mirrors the checkjs.ResolveChecker indirection pattern.
var ConnectionResolverFunc ConnectionResolver

// resolverCtxKeyType makes the context-key unique within the binary.
type resolverCtxKeyType struct{}

//nolint:gochecknoglobals // sentinel value for context key
var resolverCtxKey = resolverCtxKeyType{}

// WithResolver returns a context that carries the given resolver, taking
// precedence over the package-level ConnectionResolverFunc. Intended for
// tests — production wires the global once at startup.
func WithResolver(ctx context.Context, resolver ConnectionResolver) context.Context {
	return context.WithValue(ctx, resolverCtxKey, resolver)
}

// resolverFromContext returns the per-context resolver if set, otherwise
// the package-level ConnectionResolverFunc.
func resolverFromContext(ctx context.Context) ConnectionResolver {
	if v, ok := ctx.Value(resolverCtxKey).(ConnectionResolver); ok && v != nil {
		return v
	}

	return ConnectionResolverFunc
}

// Default per-execution timeout. The freebox HTTP client itself has a
// 30s per-request timeout (DefaultTimeout in the freebox package); we
// cap the whole check at 45s to leave room for a session refresh + two
// API calls (connection state + line-stats endpoint).
const defaultExecuteTimeout = 45 * time.Second

// FreeboxLineChecker implements checkerdef.Checker for freebox_line.
type FreeboxLineChecker struct{}

// Type returns the check type identifier.
func (c *FreeboxLineChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeFreeboxLine
}

// Validate runs config-only validation. Networking is reserved for Execute.
func (c *FreeboxLineChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &FreeboxLineConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Name == "" {
		spec.Name = "Freebox line quality"
	}

	if spec.Slug == "" {
		spec.Slug = "freebox-line-" + cfg.LinkType
	}

	return nil
}

// connectionResponse models the relevant subset of GET /api/v4/connection/.
//
//nolint:tagliatelle // JSON tags must match Freebox API field names
type connectionResponse struct {
	State         string `json:"state"`
	Type          string `json:"type"`
	RateUp        int64  `json:"rate_up"`
	RateDown      int64  `json:"rate_down"`
	BandwidthUp   int64  `json:"bandwidth_up"`
	BandwidthDown int64  `json:"bandwidth_down"`
	IPv4          string `json:"ipv4"`
	IPv6          string `json:"ipv6"`
}

// xdslResponse models GET /api/v4/connection/xdsl/.
//
//nolint:tagliatelle // JSON tags must match Freebox API field names
type xdslResponse struct {
	Status xdslStatus `json:"status"`
	Down   xdslLane   `json:"down"`
	Up     xdslLane   `json:"up"`
}

//nolint:tagliatelle // JSON tags must match Freebox API field names
type xdslStatus struct {
	Status     string `json:"status"`
	Modulation string `json:"modulation"`
	Uptime     int64  `json:"uptime"`
}

//nolint:tagliatelle // JSON tags must match Freebox API field names
type xdslLane struct {
	MaxRate int   `json:"maxrate"`
	Rate    int   `json:"rate"`
	SNR     int   `json:"snr"`
	Attn    int   `json:"attn"`
	FEC     int64 `json:"fec"`
	CRC     int64 `json:"crc"`
	HEC     int64 `json:"hec"`
	ES      int64 `json:"es"`
	SES     int64 `json:"ses"`
}

// ftthResponse models GET /api/v4/connection/ftth/.
//
//nolint:tagliatelle // JSON tags must match Freebox API field names
type ftthResponse struct {
	SfpPresent        bool   `json:"sfp_present"`
	SfpAlimOK         bool   `json:"sfp_alim_ok"`
	SfpHasPowerReport bool   `json:"sfp_has_power_report"`
	SfpHasSignal      bool   `json:"sfp_has_signal"`
	Link              bool   `json:"link"`
	SfpSerial         string `json:"sfp_serial"`
	SfpVendor         string `json:"sfp_vendor"`
	SfpModel          string `json:"sfp_model"`
	// SfpPwrTx / SfpPwrRx are reported in µW × 100 (i.e. units of 0.01 µW
	// per the Freebox docs). We convert to mW + dBm in code.
	SfpPwrTx int `json:"sfp_pwr_tx"`
	SfpPwrRx int `json:"sfp_pwr_rx"`
}

// Output keys exported on the Result.Output map so the dashboard can
// render structured readings without re-parsing.
const (
	OutputKeyState           = "state"
	OutputKeyType            = "type"
	OutputKeyDegraded        = "degraded"
	OutputKeyDegradedReason  = "degradedReason"
	OutputKeySyncRateDown    = "syncRateDownKbps"
	OutputKeySyncRateUp      = "syncRateUpKbps"
	OutputKeySnrMarginDown   = "snrMarginDownDb"
	OutputKeySnrMarginUp     = "snrMarginUpDb"
	OutputKeyAttenuationDown = "attenuationDownDb"
	OutputKeyAttenuationUp   = "attenuationUpDb"
	OutputKeyCrcErrors       = "crcErrors"
	OutputKeyFecErrors       = "fecErrors"
	OutputKeyUptimeSeconds   = "uptimeSeconds"
	OutputKeyXDSLStatus      = "xdslStatus"
	OutputKeyModulation      = "modulation"
	OutputKeyLink            = "link"
	OutputKeySfpHasSignal    = "sfpHasSignal"
	OutputKeyRxPowerMw       = "rxPowerMw"
	OutputKeyTxPowerMw       = "txPowerMw"
	OutputKeyRxPowerDbm      = "rxPowerDbm"
	OutputKeyTxPowerDbm      = "txPowerDbm"
	OutputKeySfpVendor       = "sfpVendor"
)

// xDSL status string returned by /connection/xdsl/ when the modem has
// trained successfully — every other value means "no usable line".
const xdslStatusShowtime = "showtime"

// Execute hits the WAN endpoint first; if WAN is down we surface that
// immediately without touching the line-quality endpoints. Otherwise we
// branch into xDSL / FTTH parsing and apply the configured thresholds.
func (c *FreeboxLineChecker) Execute(
	ctx context.Context, config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*FreeboxLineConfig](config)
	if err != nil {
		return nil, err
	}

	resolver := resolverFromContext(ctx)
	if resolver == nil {
		return nil, ErrResolverNotConfigured
	}

	ctx, cancel := context.WithTimeout(ctx, defaultExecuteTimeout)
	defer cancel()

	start := time.Now()

	conn, err := resolver(ctx, cfg.ConnectionUID)
	if err != nil {
		return errorResult(start, fmt.Errorf("resolve freebox connection: %w", err)), nil
	}

	client := freebox.NewClientWithAppID(conn.BaseURL, conn.AppID, conn.AppToken)

	var wan connectionResponse
	if err := client.Get(ctx, "/api/v4/connection/", &wan); err != nil {
		return downResult(start, "fetch /connection/: "+err.Error()), nil
	}

	if wan.State != "up" {
		out := map[string]any{
			OutputKeyState: wan.State,
			OutputKeyType:  wan.Type,
		}

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Output: mergeMaps(out, map[string]any{
				checkerdef.OutputKeyError: "WAN state is " + emptyAsUnknown(wan.State),
			}),
			Metrics: map[string]any{},
		}, nil
	}

	switch cfg.LinkType {
	case LinkTypeXDSL:
		return c.executeXDSL(ctx, client, cfg, &wan, start), nil
	case LinkTypeFTTH:
		return c.executeFTTH(ctx, client, cfg, &wan, start), nil
	default:
		return errorResult(start, fmt.Errorf("unknown linkType: %s", cfg.LinkType)), nil
	}
}

// executeXDSL fetches /connection/xdsl/ and applies the xDSL thresholds.
func (c *FreeboxLineChecker) executeXDSL(
	ctx context.Context,
	client *freebox.Client,
	cfg *FreeboxLineConfig,
	wan *connectionResponse,
	start time.Time,
) *checkerdef.Result {
	var resp xdslResponse
	if err := client.Get(ctx, "/api/v4/connection/xdsl/", &resp); err != nil {
		return downResult(start, "fetch /connection/xdsl/: "+err.Error())
	}

	// SNR / Attn are reported in tenths of a dB.
	snrDownDb := float64(resp.Down.SNR) / 10.0
	snrUpDb := float64(resp.Up.SNR) / 10.0
	attnDownDb := float64(resp.Down.Attn) / 10.0
	attnUpDb := float64(resp.Up.Attn) / 10.0

	output := map[string]any{
		OutputKeyState:           wan.State,
		OutputKeyType:            LinkTypeXDSL,
		OutputKeyXDSLStatus:      resp.Status.Status,
		OutputKeyModulation:      resp.Status.Modulation,
		OutputKeyUptimeSeconds:   resp.Status.Uptime,
		OutputKeySyncRateDown:    resp.Down.Rate,
		OutputKeySyncRateUp:      resp.Up.Rate,
		OutputKeySnrMarginDown:   snrDownDb,
		OutputKeySnrMarginUp:     snrUpDb,
		OutputKeyAttenuationDown: attnDownDb,
		OutputKeyAttenuationUp:   attnUpDb,
		OutputKeyCrcErrors:       resp.Down.CRC,
		OutputKeyFecErrors:       resp.Down.FEC,
	}

	metrics := map[string]any{
		"sync_rate_down_kbps_val": float64(resp.Down.Rate),
		"sync_rate_up_kbps_val":   float64(resp.Up.Rate),
		"snr_margin_down_db_val":  snrDownDb,
		"snr_margin_up_db_val":    snrUpDb,
		"attenuation_down_db_val": attnDownDb,
		"crc_errors_cnt":          float64(resp.Down.CRC),
	}

	if resp.Status.Status != xdslStatusShowtime {
		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Output: mergeMaps(output, map[string]any{
				checkerdef.OutputKeyError: "xDSL not at showtime: " + emptyAsUnknown(resp.Status.Status),
			}),
			Metrics: metrics,
		}
	}

	if reason := evaluateXDSLThresholds(cfg, &resp, snrDownDb, attnDownDb); reason != "" {
		output[OutputKeyDegraded] = true
		output[OutputKeyDegradedReason] = reason

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Output: mergeMaps(output, map[string]any{
				checkerdef.OutputKeyError: reason,
			}),
			Metrics: metrics,
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Output:   output,
		Metrics:  metrics,
	}
}

// evaluateXDSLThresholds returns the first failing threshold reason, or
// "" if every configured threshold is satisfied.
func evaluateXDSLThresholds(
	cfg *FreeboxLineConfig,
	resp *xdslResponse,
	snrDownDb, attnDownDb float64,
) string {
	if cfg.MinSyncRateDownKbps > 0 && resp.Down.Rate < cfg.MinSyncRateDownKbps {
		return fmt.Sprintf(
			"downstream sync rate %d kbps below threshold %d kbps",
			resp.Down.Rate, cfg.MinSyncRateDownKbps,
		)
	}

	if cfg.MinSnrMarginDownDb > 0 && snrDownDb < float64(cfg.MinSnrMarginDownDb) {
		return fmt.Sprintf(
			"downstream SNR margin %.1f dB below threshold %d dB",
			snrDownDb, cfg.MinSnrMarginDownDb,
		)
	}

	if cfg.MaxAttenuationDb > 0 && attnDownDb > float64(cfg.MaxAttenuationDb) {
		return fmt.Sprintf(
			"downstream attenuation %.1f dB above threshold %d dB",
			attnDownDb, cfg.MaxAttenuationDb,
		)
	}

	if cfg.MaxCrcErrorsPerRun > 0 && resp.Down.CRC > int64(cfg.MaxCrcErrorsPerRun) {
		return fmt.Sprintf(
			"downstream CRC errors %d above threshold %d",
			resp.Down.CRC, cfg.MaxCrcErrorsPerRun,
		)
	}

	return ""
}

// executeFTTH fetches /connection/ftth/ and applies the FTTH thresholds.
func (c *FreeboxLineChecker) executeFTTH(
	ctx context.Context,
	client *freebox.Client,
	cfg *FreeboxLineConfig,
	wan *connectionResponse,
	start time.Time,
) *checkerdef.Result {
	var resp ftthResponse
	if err := client.Get(ctx, "/api/v4/connection/ftth/", &resp); err != nil {
		return downResult(start, "fetch /connection/ftth/: "+err.Error())
	}

	// SFP power readings are exposed as integers in 0.01 µW units
	// (Freebox API convention). Convert to mW for thresholding and to
	// dBm for human-friendly display.
	rxPowerMw := uwToMw(resp.SfpPwrRx)
	txPowerMw := uwToMw(resp.SfpPwrTx)

	output := map[string]any{
		OutputKeyState:        wan.State,
		OutputKeyType:         LinkTypeFTTH,
		OutputKeyLink:         resp.Link,
		OutputKeySfpHasSignal: resp.SfpHasSignal,
		OutputKeyRxPowerMw:    rxPowerMw,
		OutputKeyTxPowerMw:    txPowerMw,
		OutputKeyRxPowerDbm:   mwToDbm(rxPowerMw),
		OutputKeyTxPowerDbm:   mwToDbm(txPowerMw),
		OutputKeySfpVendor:    resp.SfpVendor,
	}

	metrics := map[string]any{
		"rx_power_mw_val":  rxPowerMw,
		"tx_power_mw_val":  txPowerMw,
		"rx_power_dbm_val": mwToDbm(rxPowerMw),
	}

	if !resp.Link || !resp.SfpHasSignal {
		reason := "FTTH no signal"
		if !resp.Link {
			reason = "FTTH link down"
		}

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Output: mergeMaps(output, map[string]any{
				checkerdef.OutputKeyError: reason,
			}),
			Metrics: metrics,
		}
	}

	if reason := evaluateFTTHThresholds(cfg, rxPowerMw); reason != "" {
		output[OutputKeyDegraded] = true
		output[OutputKeyDegradedReason] = reason

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Output: mergeMaps(output, map[string]any{
				checkerdef.OutputKeyError: reason,
			}),
			Metrics: metrics,
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Output:   output,
		Metrics:  metrics,
	}
}

// evaluateFTTHThresholds returns the first failing threshold reason, or
// "" if every configured threshold is satisfied.
func evaluateFTTHThresholds(cfg *FreeboxLineConfig, rxPowerMw float64) string {
	if cfg.MinRxPowerMw > 0 && rxPowerMw < cfg.MinRxPowerMw {
		return fmt.Sprintf(
			"Rx power %.4f mW below threshold %.4f mW",
			rxPowerMw, cfg.MinRxPowerMw,
		)
	}

	if cfg.MaxRxPowerMw > 0 && rxPowerMw > cfg.MaxRxPowerMw {
		return fmt.Sprintf(
			"Rx power %.4f mW above threshold %.4f mW",
			rxPowerMw, cfg.MaxRxPowerMw,
		)
	}

	return ""
}

// uwToMw converts the Freebox SFP-power raw integer (0.01 µW units) to mW.
// 1 mW = 1000 µW = 100 000 raw-units.
func uwToMw(raw int) float64 {
	const rawPerMw = 100_000.0
	return float64(raw) / rawPerMw
}

// mwToDbm converts a power in mW to dBm. Returns -inf for non-positive
// inputs; the caller is responsible for sanity checks downstream (we
// never display the raw value blindly).
func mwToDbm(mw float64) float64 {
	if mw <= 0 {
		return math.Inf(-1)
	}

	return 10 * math.Log10(mw)
}

// emptyAsUnknown reports `s` as-is unless it is empty, in which case
// "unknown" is returned. Avoids noisy "WAN state is" messages.
func emptyAsUnknown(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}

	return s
}

// mergeMaps returns a new map composed of all keys from base + extras.
// Extras win on conflict — matches the OutputKeyError append pattern.
func mergeMaps(base, extras map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extras))
	for k, v := range base {
		out[k] = v
	}

	for k, v := range extras {
		out[k] = v
	}

	return out
}

// downResult builds a `Down` result carrying a single error message.
func downResult(start time.Time, msg string) *checkerdef.Result {
	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Output: map[string]any{
			checkerdef.OutputKeyError: msg,
		},
		Metrics: map[string]any{},
	}
}

// errorResult builds a Status=Error result (internal failure rather than
// a meaningful "the line is bad" outcome).
func errorResult(start time.Time, err error) *checkerdef.Result {
	return &checkerdef.Result{
		Status:   checkerdef.StatusError,
		Duration: time.Since(start),
		Output: map[string]any{
			checkerdef.OutputKeyError: err.Error(),
		},
		Metrics: map[string]any{},
	}
}

// Ensure FreeboxLineChecker satisfies the Checker interface.
var _ checkerdef.Checker = (*FreeboxLineChecker)(nil)

// Sentinel — only used in tests today, but useful for callers that want
// to distinguish "resolver returned a known not-found" from any other
// error chain.
var ErrConnectionNotFound = errors.New("freebox: connection not found")
