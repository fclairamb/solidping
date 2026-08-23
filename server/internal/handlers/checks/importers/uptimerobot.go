package importers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// jsonNull is the JSON literal for null, checked in a couple of places where
// UptimeRobot represents "unset" as an explicit null rather than omitting the
// field.
const jsonNull = "null"

// errNotJSONObjectOrArray is returned when the top-level input is neither a
// JSON object nor a JSON array.
var errNotJSONObjectOrArray = errors.New("uptimerobot payload is neither a JSON object nor an array")

// UptimeRobotConverter turns an UptimeRobot API v2 `getMonitors` response into
// an ExportDocument. Users fetch the JSON themselves with a read-only API key
// (documented in the migration guide) and paste or upload it — there is no
// outbound fetch here, unlike the Better Stack converter.
type UptimeRobotConverter struct{}

// Source implements Converter.
func (c *UptimeRobotConverter) Source() string { return SourceUptimeRobot }

// robotResponse is the raw shape of one `getMonitors` page.
type robotResponse struct {
	Stat     string         `json:"stat"`
	Monitors []robotMonitor `json:"monitors"`
}

// robotMonitor mirrors one entry of a getMonitors response.
//
//nolint:tagliatelle // field names are UptimeRobot's, not ours
type robotMonitor struct {
	ID           int64        `json:"id"`
	FriendlyName string       `json:"friendly_name"`
	URL          string       `json:"url"`
	Type         int          `json:"type"`
	SubType      robotFlexInt `json:"sub_type"`
	Port         robotFlexInt `json:"port"`
	KeywordType  robotFlexInt `json:"keyword_type"`
	KeywordValue string       `json:"keyword_value"`
	HTTPUsername string       `json:"http_username"`
	HTTPPassword string       `json:"http_password"`
	HTTPAuthType robotFlexInt `json:"http_auth_type"`
	Interval     int          `json:"interval"`
	Timeout      int          `json:"timeout"`
	Status       int          `json:"status"`

	AlertContacts      []json.RawMessage `json:"alert_contacts"`
	MWindows           []json.RawMessage `json:"mwindows"`
	CustomHTTPStatuses string            `json:"custom_http_statuses"`
	CustomHTTPHeaders  json.RawMessage   `json:"custom_http_headers"`
	PSP                json.RawMessage   `json:"psp"`
}

// robotFlexInt decodes an integer that UptimeRobot's API renders inconsistently
// as either a JSON number or a numeric string (notably `sub_type` and `port`
// on Port monitors). An absent, null or empty value decodes to zero.
type robotFlexInt int

// UnmarshalJSON implements json.Unmarshaler.
func (f *robotFlexInt) UnmarshalJSON(data []byte) error {
	trimmed := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if trimmed == "" || trimmed == jsonNull {
		*f = 0

		return nil
	}

	n, err := strconv.Atoi(trimmed)
	if err != nil {
		return fmt.Errorf("invalid integer %q: %w", trimmed, err)
	}

	*f = robotFlexInt(n)

	return nil
}

// UptimeRobot monitor `type` values.
const (
	robotTypeHTTP      = 1
	robotTypeKeyword   = 2
	robotTypePing      = 3
	robotTypePort      = 4
	robotTypeHeartbeat = 5
)

// UptimeRobot Port monitor `sub_type` values. 99 means "custom", read from the
// `port` field instead.
const (
	robotSubTypeCustom = 99
)

// robotSubTypePorts maps a well-known Port monitor sub_type to its port.
//
//nolint:gochecknoglobals // robotSubTypePorts is a constant lookup map.
var robotSubTypePorts = map[int]int{
	1: 80,  // HTTP
	2: 443, // HTTPS
	3: 21,  // FTP
	4: 25,  // SMTP
	5: 110, // POP3
	6: 143, // IMAP
}

// UptimeRobot `keyword_type` values.
const (
	robotKeywordExists    = 1
	robotKeywordNotExists = 2
)

// UptimeRobot `http_auth_type` values.
const (
	robotAuthBasic = 1
)

// robotStatusPaused is the `status` value for a paused monitor.
const robotStatusPaused = 0

// Convert implements Converter.
func (c *UptimeRobotConverter) Convert(input []byte) (*ConversionResult, error) {
	monitors, err := decodeRobotMonitors(input)
	if err != nil {
		return nil, err
	}

	if len(monitors) == 0 {
		return nil, fmt.Errorf("%w: the response has no monitors", ErrEmptyInput)
	}

	warn := &warnings{}
	doc := newDocument(SourceUptimeRobot)
	slugs := newSlugSet()

	notified := false

	for idx := range monitors {
		monitor := &monitors[idx]

		if (len(monitor.AlertContacts) > 0 || len(monitor.MWindows) > 0) && !notified {
			warn.addDocf("alert contacts and maintenance windows are not imported — configure " +
				"SolidPing integrations and maintenance windows instead")

			notified = true
		}

		if converted, ok := c.convertMonitor(monitor, slugs, warn); ok {
			doc.Checks = append(doc.Checks, converted)
		}
	}

	if len(doc.Checks) == 0 {
		return nil, fmt.Errorf("%w: no UptimeRobot monitor could be mapped to a SolidPing check", ErrEmptyInput)
	}

	normalizeChecks(doc, warn)

	return &ConversionResult{Document: doc, Warnings: warn.result()}, nil
}

// decodeRobotMonitors accepts the three shapes documented for this converter:
// the raw response object, a bare monitors array, or an array of page objects
// (concatenated paginated responses) — so users can paste whatever their
// export produced.
func decodeRobotMonitors(input []byte) ([]robotMonitor, error) {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("%w: the input is empty", ErrEmptyInput)
	}

	switch trimmed[0] {
	case '{':
		var resp robotResponse
		if err := json.Unmarshal(trimmed, &resp); err != nil {
			return nil, fmt.Errorf("parse uptimerobot response: %w", err)
		}

		return resp.Monitors, nil
	case '[':
		return decodeRobotMonitorArray(trimmed)
	default:
		return nil, fmt.Errorf("parse uptimerobot response: %w", errNotJSONObjectOrArray)
	}
}

// decodeRobotMonitorArray decodes a top-level JSON array, disambiguating a
// bare monitors array from an array of paginated response pages by peeking at
// whether the first element carries a "monitors" key.
func decodeRobotMonitorArray(trimmed []byte) ([]robotMonitor, error) {
	var probe []json.RawMessage
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return nil, fmt.Errorf("parse uptimerobot response: %w", err)
	}

	if len(probe) == 0 {
		return nil, nil
	}

	var firstKeys map[string]json.RawMessage
	if err := json.Unmarshal(probe[0], &firstKeys); err != nil {
		return nil, fmt.Errorf("parse uptimerobot response: %w", err)
	}

	if _, isPage := firstKeys["monitors"]; isPage {
		monitors := make([]robotMonitor, 0, len(probe))

		for _, raw := range probe {
			var page robotResponse
			if err := json.Unmarshal(raw, &page); err != nil {
				return nil, fmt.Errorf("parse uptimerobot paginated response: %w", err)
			}

			monitors = append(monitors, page.Monitors...)
		}

		return monitors, nil
	}

	var monitors []robotMonitor
	if err := json.Unmarshal(trimmed, &monitors); err != nil {
		return nil, fmt.Errorf("parse uptimerobot monitors array: %w", err)
	}

	return monitors, nil
}

// convertMonitor maps one UptimeRobot monitor to a check. Returns false when
// the monitor type has no SolidPing counterpart (a warning is recorded).
func (c *UptimeRobotConverter) convertMonitor(
	monitor *robotMonitor, slugs *slugSet, warn *warnings,
) (checks.ExportCheck, bool) {
	name := monitor.FriendlyName
	if name == "" {
		name = monitor.URL
	}

	check := checks.ExportCheck{
		Name:    name,
		Slug:    slugs.unique(name),
		Enabled: monitor.Status != robotStatusPaused,
		Period:  secondsToPeriod(monitor.Interval),
		Config:  map[string]any{},
	}

	timeout := secondsToPeriod(monitor.Timeout)

	var ok bool

	switch monitor.Type {
	case robotTypeHTTP, robotTypeKeyword:
		check.Type = string(checkerdef.CheckTypeHTTP)
		check.Config = c.httpConfig(monitor, timeout, warn)
		ok = true
	case robotTypePing:
		check.Type = string(checkerdef.CheckTypeICMP)
		check.Config = map[string]any{cfgKeyHost: monitor.URL}

		if timeout != "" {
			check.Config[cfgKeyTimeout] = timeout
		}

		ok = true
	case robotTypePort:
		check.Type = string(checkerdef.CheckTypeTCP)
		check.Config = c.portConfig(monitor, timeout, name, warn)
		ok = true
	case robotTypeHeartbeat:
		check.Type = string(checkerdef.CheckTypeHeartbeat)
		check.Config = map[string]any{}

		warn.addf(name, "type", "heartbeat monitor imported as a SolidPing heartbeat check — "+
			"the ping URL changes, repoint the job that pushes to it after the import")

		ok = true
	default:
		warn.addf(name, "type", "monitor skipped: UptimeRobot type %d has no SolidPing counterpart", monitor.Type)
	}

	if !ok {
		return checks.ExportCheck{}, false
	}

	c.warnUnmapped(monitor, name, warn)

	return check, true
}

// warnUnmapped reports monitor-level settings with no SolidPing counterpart.
func (c *UptimeRobotConverter) warnUnmapped(monitor *robotMonitor, name string, warn *warnings) {
	if monitor.CustomHTTPStatuses != "" {
		warn.addf(name, "custom_http_statuses",
			"custom HTTP status rules are not imported — the check uses the default 2xx expectation")
	}

	if len(monitor.CustomHTTPHeaders) > 0 && !isEmptyJSON(monitor.CustomHTTPHeaders) {
		warn.addf(name, "custom_http_headers", "custom HTTP headers are not imported")
	}

	if len(monitor.PSP) > 0 && !isEmptyJSON(monitor.PSP) {
		warn.addf(name, "psp", "public status page (PSP) settings are not imported")
	}
}

// isEmptyJSON reports whether a raw JSON value is absent, null, or an empty
// object/array/string — the shapes UptimeRobot uses to mean "nothing here".
func isEmptyJSON(raw json.RawMessage) bool {
	switch strings.TrimSpace(string(raw)) {
	case "", jsonNull, "{}", "[]", `""`:
		return true
	default:
		return false
	}
}

// httpConfig builds the http checker config for HTTP(s) and Keyword monitors.
func (c *UptimeRobotConverter) httpConfig(monitor *robotMonitor, timeout string, warn *warnings) map[string]any {
	cfg := map[string]any{cfgKeyURL: monitor.URL}

	if timeout != "" {
		cfg[cfgKeyTimeout] = timeout
	}

	if int(monitor.HTTPAuthType) == robotAuthBasic && monitor.HTTPUsername != "" {
		cfg["basicAuth"] = monitor.HTTPUsername + ":" + monitor.HTTPPassword
	} else if int(monitor.HTTPAuthType) != 0 && monitor.HTTPUsername != "" {
		warn.addf(monitor.FriendlyName, "http_auth_type",
			"only basic HTTP authentication is imported — re-enter this monitor's credential on the check")
	}

	if monitor.Type == robotTypeKeyword {
		switch int(monitor.KeywordType) {
		case robotKeywordExists:
			cfg["body_expect"] = monitor.KeywordValue
		case robotKeywordNotExists:
			cfg["body_reject"] = monitor.KeywordValue
		}
	}

	return cfg
}

// portConfig builds the tcp checker config for Port monitors. Well-known
// sub_types (1-6) resolve to their standard port; sub_type 99 (custom) reads
// the `port` field.
func (c *UptimeRobotConverter) portConfig(monitor *robotMonitor, timeout, name string, warn *warnings) map[string]any {
	cfg := map[string]any{cfgKeyHost: monitor.URL}

	if port, ok := robotPortFor(monitor, name, warn); ok {
		cfg[cfgKeyPort] = port
	}

	if timeout != "" {
		cfg[cfgKeyTimeout] = timeout
	}

	return cfg
}

// robotPortFor resolves the port for a Port monitor: a well-known sub_type
// (1-6) resolves to its standard port; sub_type 99 (custom) or an absent
// sub_type reads the `port` field; any other sub_type falls back to the
// `port` field too, with a warning.
func robotPortFor(monitor *robotMonitor, name string, warn *warnings) (int, bool) {
	subType := int(monitor.SubType)

	if port, known := robotSubTypePorts[subType]; known {
		return port, true
	}

	if subType != robotSubTypeCustom && subType != 0 {
		warn.addf(name, "sub_type", "unknown port sub_type %d — the port field was used instead", subType)
	}

	if port := int(monitor.Port); port > 0 {
		return port, true
	}

	warn.addf(name, "port", "port monitor has no resolvable port and was imported without one")

	return 0, false
}
