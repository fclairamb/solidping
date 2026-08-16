package checkprometheus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Prometheus HTTP API result types.
const (
	resultTypeVector = "vector"
	resultTypeScalar = "scalar"
	resultTypeMatrix = "matrix"
	resultTypeString = "string"
)

// promQLResponse is the shape of a Prometheus /api/v1/query response. The
// `data.result` payload differs by result type, so it stays raw until the type
// is known.
type promQLResponse struct {
	Status    string          `json:"status"`
	ErrorType string          `json:"errorType"`
	Error     string          `json:"error"`
	Data      promQLResultRaw `json:"data"`
}

type promQLResultRaw struct {
	ResultType string          `json:"resultType"`
	Result     json.RawMessage `json:"result"`
}

// promQLVectorSample is one instant-vector sample: a label set and a
// [timestamp, "value"] pair.
type promQLVectorSample struct {
	Metric map[string]string `json:"metric"`
	Value  []json.RawMessage `json:"value"`
}

var (
	errPromQLStatus  = errors.New("prometheus query failed")
	errPromQLDecode  = errors.New("failed to decode prometheus response")
	errPromQLSample  = errors.New("malformed prometheus sample")
	errMatrixResult  = errors.New("matrix result is not supported")
	errStringResult  = errors.New("string result is not supported")
	errUnknownResult = errors.New("unknown prometheus result type")
)

// queryURL builds the instant-query URL from the configured server base URL.
func queryURL(base, query string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}

	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/api/v1/query"

	values := parsed.Query()
	values.Set("query", query)
	parsed.RawQuery = values.Encode()

	return parsed.String(), nil
}

// fetchPromQL runs the instant query and resolves it to a single value.
//
// It accepts scalar and instant-vector results. A `matrix` (range) result is
// rejected rather than silently reduced: a range query returns a series of
// points over time and there is no defensible single "current value" to grade
// — the fix is an instant query, so the error says so.
func fetchPromQL(ctx context.Context, cfg *PrometheusConfig) (*resolution, error) {
	target, err := queryURL(cfg.URL, cfg.Query)
	if err != nil {
		return nil, err
	}

	resp, err := doGet(ctx, cfg, target)
	if err != nil {
		return nil, err
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := readCapped(resp.Body)
	if err != nil {
		return nil, err
	}

	// A Prometheus error is served with a 4xx/5xx *and* a JSON body naming
	// the cause, so decode first and only fall back to the status code.
	var decoded promQLResponse
	if jsonErr := json.Unmarshal(body, &decoded); jsonErr != nil {
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%w: %d", errNon200, resp.StatusCode)
		}

		return nil, fmt.Errorf("%w: %w", errPromQLDecode, jsonErr)
	}

	if decoded.Status != "success" {
		return nil, fmt.Errorf("%w: %s: %s", errPromQLStatus, decoded.ErrorType, decoded.Error)
	}

	return resolvePromQLData(cfg, decoded.Data)
}

// resolvePromQLData turns the typed result payload into a resolution.
func resolvePromQLData(cfg *PrometheusConfig, data promQLResultRaw) (*resolution, error) {
	switch data.ResultType {
	case resultTypeScalar:
		var raw []json.RawMessage
		if err := json.Unmarshal(data.Result, &raw); err != nil {
			return nil, fmt.Errorf("%w: %w", errPromQLDecode, err)
		}

		value, err := sampleValue(raw)
		if err != nil {
			return nil, err
		}

		return &resolution{Value: value, Matched: 1, Labels: "{}"}, nil

	case resultTypeVector:
		return resolvePromQLVector(cfg, data.Result)

	case resultTypeMatrix:
		return nil, fmt.Errorf(
			"%w: use an instant query (a matrix returns a range of points, not one current value)",
			errMatrixResult,
		)

	case resultTypeString:
		return nil, fmt.Errorf("%w: the query must return a number", errStringResult)

	default:
		return nil, fmt.Errorf("%w: %q", errUnknownResult, data.ResultType)
	}
}

// resolvePromQLVector reduces an instant vector according to `match`.
func resolvePromQLVector(cfg *PrometheusConfig, raw json.RawMessage) (*resolution, error) {
	var samples []promQLVectorSample
	if err := json.Unmarshal(raw, &samples); err != nil {
		return nil, fmt.Errorf("%w: %w", errPromQLDecode, err)
	}

	if len(samples) == 0 {
		return &resolution{Matched: 0}, nil
	}

	matched := make([]series, 0, len(samples))

	for _, sample := range samples {
		value, err := sampleValue(sample.Value)
		if err != nil {
			return nil, err
		}

		matched = append(matched, series{Name: cfg.Query, Labels: sample.Metric, Value: value})
	}

	return resolveMatched(cfg, matched)
}

// sampleValue extracts the numeric half of a [timestamp, "value"] pair. The
// Prometheus API serializes the value as a JSON string.
func sampleValue(raw []json.RawMessage) (float64, error) {
	//nolint:mnd // the API pair is exactly [timestamp, "value"]
	if len(raw) != 2 {
		return 0, fmt.Errorf("%w: expected [timestamp, value], got %d elements", errPromQLSample, len(raw))
	}

	var asString string
	if err := json.Unmarshal(raw[1], &asString); err == nil {
		value, convErr := strconv.ParseFloat(asString, 64)
		if convErr != nil {
			return 0, fmt.Errorf("%w: %q is not a number", errPromQLSample, asString)
		}

		return value, nil
	}

	var asNumber float64
	if err := json.Unmarshal(raw[1], &asNumber); err != nil {
		return 0, fmt.Errorf("%w: value is neither a string nor a number", errPromQLSample)
	}

	return asNumber, nil
}
