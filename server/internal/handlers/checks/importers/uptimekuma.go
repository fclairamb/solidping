package importers

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhttp"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// UptimeKumaConverter turns an Uptime Kuma 1.x backup JSON
// (Settings → Backup → Export) into an ExportDocument.
//
// Kuma's own API is socket.io with session auth, so it is not practically
// fetchable server-side; the backup file is the input. Kuma 2.x dropped the
// JSON backup export — 2.x users need a 1.x-compatible export.
type UptimeKumaConverter struct{}

// Source implements Converter.
func (c *UptimeKumaConverter) Source() string { return SourceUptimeKuma }

// kumaBackup is the subset of a Kuma backup file we read.
type kumaBackup struct {
	Version     string        `json:"version"`
	MonitorList []kumaMonitor `json:"monitorList"`
}

// kumaMonitor mirrors one entry of the Kuma monitorList.
//
//nolint:tagliatelle // field names are Uptime Kuma's, not ours
type kumaMonitor struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Active      *bool  `json:"active"`
	Parent      *int   `json:"parent"`

	URL      string `json:"url"`
	Hostname string `json:"hostname"`
	Port     *int   `json:"port"`
	Method   string `json:"method"`
	Body     string `json:"body"`
	Headers  string `json:"headers"`

	Interval      int `json:"interval"`
	RetryInterval int `json:"retryInterval"`
	MaxRetries    int `json:"maxretries"`
	Timeout       int `json:"timeout"`

	Keyword            string   `json:"keyword"`
	InvertKeyword      bool     `json:"invertKeyword"`
	JSONPath           string   `json:"jsonPath"`
	ExpectedValue      string   `json:"expectedValue"`
	AcceptedStatusCode []string `json:"accepted_statuscodes"`

	IgnoreTLS  bool `json:"ignoreTls"`
	UpsideDown bool `json:"upsideDown"`

	DNSResolveServer string `json:"dns_resolve_server"`
	DNSResolveType   string `json:"dns_resolve_type"`

	DockerContainer string `json:"docker_container"`

	MQTTTopic          string `json:"mqttTopic"`
	MQTTUsername       string `json:"mqttUsername"`
	MQTTPassword       string `json:"mqttPassword"`
	MQTTSuccessMessage string `json:"mqttSuccessMessage"`

	DatabaseConnectionString string `json:"databaseConnectionString"`
	DatabaseQuery            string `json:"databaseQuery"`

	GRPCUrl         string `json:"grpcUrl"`
	GRPCServiceName string `json:"grpcServiceName"`
	GRPCEnableTLS   bool   `json:"grpcEnableTls"`

	AuthMethod    string `json:"authMethod"`
	BasicAuthUser string `json:"basic_auth_user"`
	BasicAuthPass string `json:"basic_auth_pass"`

	NotificationIDList map[string]bool `json:"notificationIDList"`
	Tags               []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"tags"`
}

// kumaGroupType is the Kuma monitor type that models a folder rather than a check.
const kumaGroupType = "group"

// kumaStatusRangeRe matches an "NXX-style" accepted status range (200-299).
var kumaStatusRangeRe = regexp.MustCompile(`^([1-5])00-([1-5])99$`)

// Convert implements Converter.
func (c *UptimeKumaConverter) Convert(input []byte) (*ConversionResult, error) {
	var backup kumaBackup
	if err := json.Unmarshal(input, &backup); err != nil {
		return nil, fmt.Errorf("parse uptime kuma backup json: %w", err)
	}

	if len(backup.MonitorList) == 0 {
		return nil, fmt.Errorf("%w: the backup has no monitorList "+
			"(Uptime Kuma 2.x removed the JSON backup export — use a 1.x-compatible export)", ErrEmptyInput)
	}

	warn := &warnings{}
	doc := newDocument(SourceUptimeKuma)
	slugs := newSlugSet()
	groups := kumaGroupNames(backup.MonitorList)

	notified := false

	for idx := range backup.MonitorList {
		monitor := &backup.MonitorList[idx]
		if monitor.Type == kumaGroupType {
			continue
		}

		if len(monitor.NotificationIDList) > 0 && !notified {
			warn.addDoc("notification bindings are not imported — configure SolidPing integrations instead")

			notified = true
		}

		if converted, ok := c.convertMonitor(monitor, groups, slugs, warn); ok {
			doc.Checks = append(doc.Checks, converted)
		}
	}

	if len(doc.Checks) == 0 {
		return nil, fmt.Errorf("%w: no Uptime Kuma monitor could be mapped to a SolidPing check", ErrEmptyInput)
	}

	return &ConversionResult{Document: doc, Warnings: warn.result()}, nil
}

// kumaGroupNames indexes the group-type monitors by id so children can resolve
// their group name.
func kumaGroupNames(monitors []kumaMonitor) map[int]string {
	groups := make(map[int]string)

	for i := range monitors {
		if monitors[i].Type == kumaGroupType {
			groups[monitors[i].ID] = monitors[i].Name
		}
	}

	return groups
}

// convertMonitor maps one Kuma monitor to a check. Returns false when the
// monitor type has no SolidPing counterpart (a warning is recorded).
//
//nolint:cyclop,funlen // one dispatch per Kuma monitor type
func (c *UptimeKumaConverter) convertMonitor(
	monitor *kumaMonitor, groups map[int]string, slugs *slugSet, warn *warnings,
) (checks.ExportCheck, bool) {
	name := monitor.Name
	if name == "" {
		name = monitor.URL
	}

	check := checks.ExportCheck{
		Name:        name,
		Slug:        slugs.unique(name),
		Description: monitor.Description,
		Enabled:     monitor.Active == nil || *monitor.Active,
		Period:      secondsToPeriod(monitor.Interval),
		Config:      map[string]any{},
	}

	if monitor.Parent != nil {
		if groupName, ok := groups[*monitor.Parent]; ok {
			check.Group = groupName
		}
	}

	if confirmation := kumaConfirmationSeconds(monitor); confirmation > 0 {
		check.ConfirmationPeriodSeconds = intPtr(confirmation)
	}

	timeout := secondsToPeriod(monitor.Timeout)

	var ok bool

	switch monitor.Type {
	case "http", "keyword", "json-query":
		check.Type = string(checkerdef.CheckTypeHTTP)
		check.Config = c.httpConfig(monitor, timeout, warn)
		ok = true
	case "port":
		check.Type = string(checkerdef.CheckTypeTCP)
		check.Config = kumaHostPort(monitor, timeout)
		ok = true
	case "ping":
		check.Type = string(checkerdef.CheckTypeICMP)
		check.Config = map[string]any{"host": monitor.Hostname}
		ok = true
	case "dns":
		check.Type = string(checkerdef.CheckTypeDNS)
		check.Config = kumaDNSConfig(monitor)
		ok = true
	case "docker":
		check.Type = string(checkerdef.CheckTypeDocker)
		check.Config = map[string]any{"containerName": monitor.DockerContainer}

		warn.add(name, "docker_host", "the Docker host is not imported — the check defaults to the "+
			"worker's local Docker socket; set a custom host on the check if needed")

		ok = true
	case "push":
		check.Type = string(checkerdef.CheckTypeHeartbeat)
		check.Config = map[string]any{}

		warn.add(name, "type", "push monitor imported as a SolidPing heartbeat check — "+
			"the ping URL changes, repoint the job that pushes to it after the import")

		ok = true
	case "grpc-keyword":
		check.Type = string(checkerdef.CheckTypeGRPC)
		check.Config = kumaGRPCConfig(monitor, timeout)
		ok = true
	case "mqtt":
		check.Type = string(checkerdef.CheckTypeMQTT)
		check.Config = kumaMQTTConfig(monitor, timeout, name, warn)
		ok = true
	case "postgres", "mysql", "redis", "sqlserver", "mongodb":
		check.Type = kumaDatabaseCheckType(monitor.Type)
		check.Config, ok = kumaDatabaseConfig(monitor, timeout, name, warn)
	case "steam":
		check.Type = string(checkerdef.CheckTypeA2S)
		check.Config = kumaHostPort(monitor, timeout)
		ok = true
	case "real-browser":
		check.Type = string(checkerdef.CheckTypeBrowser)
		check.Config = map[string]any{"url": monitor.URL}
		ok = true
	default:
		warn.add(name, "type", "monitor skipped: Uptime Kuma type %q has no SolidPing counterpart", monitor.Type)
	}

	if !ok {
		return checks.ExportCheck{}, false
	}

	c.warnUnmapped(monitor, name, warn)

	return check, true
}

// warnUnmapped reports monitor-level settings with no SolidPing counterpart.
func (c *UptimeKumaConverter) warnUnmapped(monitor *kumaMonitor, name string, warn *warnings) {
	if monitor.IgnoreTLS {
		warn.add(name, "ignoreTls", "ignoring TLS errors is not supported — the check verifies certificates")
	}

	if monitor.UpsideDown {
		warn.add(name, "upsideDown", "upside-down mode has no SolidPing equivalent; the check reports normally")
	}

	if monitor.AuthMethod != "" && monitor.AuthMethod != "none" {
		warn.add(name, "authMethod",
			"authentication credentials are not imported — re-enter them on the check")
	}

	if len(monitor.Tags) > 0 {
		warn.add(name, "tags", "Uptime Kuma tags are not imported — add SolidPing labels manually")
	}
}

// kumaConfirmationSeconds turns Kuma's retry model (maxretries × retryInterval)
// into SolidPing's incident confirmation period. Kuma's `maxretries` is the
// legacy `incidentThreshold`, which the export format now expresses as a
// duration.
func kumaConfirmationSeconds(monitor *kumaMonitor) int {
	if monitor.MaxRetries <= 0 {
		return 0
	}

	retry := monitor.RetryInterval
	if retry <= 0 {
		retry = monitor.Interval
	}

	if retry <= 0 {
		return 0
	}

	return monitor.MaxRetries * retry
}

// httpConfig builds the http checker config for http / keyword / json-query
// monitors.
func (c *UptimeKumaConverter) httpConfig(monitor *kumaMonitor, timeout string, warn *warnings) map[string]any {
	cfg := map[string]any{"url": monitor.URL}

	if monitor.Method != "" {
		cfg["method"] = strings.ToUpper(monitor.Method)
	}

	if monitor.Body != "" {
		cfg["body"] = monitor.Body
	}

	if headers := kumaHeaders(monitor, warn); len(headers) > 0 {
		cfg["headers"] = headers
	}

	if timeout != "" {
		cfg["timeout"] = timeout
	}

	if codes := kumaStatusCodes(monitor, warn); len(codes) > 0 {
		cfg["expected_status_codes"] = codes
	}

	switch monitor.Type {
	case "keyword":
		if monitor.InvertKeyword {
			cfg["body_reject"] = monitor.Keyword
		} else {
			cfg["body_expect"] = monitor.Keyword
		}
	case "json-query":
		if monitor.JSONPath != "" {
			cfg["json_path_assertions"] = checkhttp.AssertionNode{
				Type:     checkhttp.NodeTypeAssertion,
				Path:     kumaJSONPath(monitor.JSONPath),
				Operator: "eq",
				Value:    monitor.ExpectedValue,
			}
		}
	}

	return cfg
}

// kumaHeaders decodes Kuma's JSON-string headers field.
func kumaHeaders(monitor *kumaMonitor, warn *warnings) map[string]any {
	raw := strings.TrimSpace(monitor.Headers)
	if raw == "" || raw == "null" {
		return nil
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		warn.add(monitor.Name, "headers", "the headers field is not valid JSON and was dropped")

		return nil
	}

	return decoded
}

// kumaStatusCodes maps Kuma accepted status ranges onto SolidPing status
// patterns ("200-299" → "2XX").
func kumaStatusCodes(monitor *kumaMonitor, warn *warnings) []any {
	codes := make([]any, 0, len(monitor.AcceptedStatusCode))

	for _, raw := range monitor.AcceptedStatusCode {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}

		if m := kumaStatusRangeRe.FindStringSubmatch(trimmed); m != nil && m[1] == m[2] {
			codes = append(codes, m[1]+"XX")

			continue
		}

		if _, err := strconv.Atoi(trimmed); err == nil {
			codes = append(codes, trimmed)

			continue
		}

		warn.add(monitor.Name, "accepted_statuscodes",
			"accepted status range %q has no SolidPing equivalent and was dropped", trimmed)
	}

	// The Kuma default (200-299) matches the SolidPing default; emitting it
	// explicitly is harmless and keeps the import faithful.
	return codes
}

// kumaHostPort builds a host/port config from a Kuma monitor.
func kumaHostPort(monitor *kumaMonitor, timeout string) map[string]any {
	cfg := map[string]any{"host": monitor.Hostname}
	if monitor.Port != nil && *monitor.Port > 0 {
		cfg["port"] = *monitor.Port
	}

	if timeout != "" {
		cfg["timeout"] = timeout
	}

	return cfg
}

// kumaDNSConfig builds the dns checker config.
func kumaDNSConfig(monitor *kumaMonitor) map[string]any {
	cfg := map[string]any{"host": monitor.Hostname}

	if monitor.DNSResolveServer != "" {
		cfg["nameserver"] = monitor.DNSResolveServer
	}

	if monitor.DNSResolveType != "" {
		cfg["record_type"] = strings.ToUpper(monitor.DNSResolveType)
	}

	return cfg
}

// kumaGRPCConfig builds the grpc checker config from Kuma's grpc-keyword fields.
func kumaGRPCConfig(monitor *kumaMonitor, timeout string) map[string]any {
	cfg := map[string]any{}

	host, port := splitHostPort(monitor.GRPCUrl)
	cfg["host"] = host

	if port > 0 {
		cfg["port"] = port
	}

	if monitor.GRPCServiceName != "" {
		cfg["serviceName"] = monitor.GRPCServiceName
	}

	if monitor.GRPCEnableTLS {
		cfg["tls"] = true
	}

	if monitor.Keyword != "" {
		cfg["keyword"] = monitor.Keyword
		if monitor.InvertKeyword {
			cfg["invertKeyword"] = true
		}
	}

	if timeout != "" {
		cfg["timeout"] = timeout
	}

	return cfg
}

// kumaMQTTConfig builds the mqtt checker config.
func kumaMQTTConfig(monitor *kumaMonitor, timeout, name string, warn *warnings) map[string]any {
	cfg := map[string]any{"host": monitor.Hostname}
	if monitor.Port != nil && *monitor.Port > 0 {
		cfg["port"] = *monitor.Port
	}

	if monitor.MQTTTopic != "" {
		cfg["topic"] = monitor.MQTTTopic
	}

	if monitor.MQTTUsername != "" {
		cfg["username"] = monitor.MQTTUsername
	}

	if monitor.MQTTPassword != "" {
		warn.add(name, "mqttPassword", "the MQTT password was not imported — re-enter it on the check")
	}

	if monitor.MQTTSuccessMessage != "" {
		warn.add(name, "mqttSuccessMessage",
			"matching an expected MQTT payload is not supported; the check verifies broker connectivity")
	}

	if timeout != "" {
		cfg["timeout"] = timeout
	}

	return cfg
}

// kumaDatabaseCheckType maps a Kuma database monitor type to a SolidPing type.
func kumaDatabaseCheckType(kumaType string) string {
	switch kumaType {
	case "postgres":
		return string(checkerdef.CheckTypePostgreSQL)
	case "mysql":
		return string(checkerdef.CheckTypeMySQL)
	case "redis":
		return string(checkerdef.CheckTypeRedis)
	case "sqlserver":
		return string(checkerdef.CheckTypeMSSQL)
	case "mongodb":
		return string(checkerdef.CheckTypeMongoDB)
	default:
		return ""
	}
}

// kumaDatabaseConfig turns a Kuma database connection string into a checker
// config. Passwords are deliberately dropped (and reported) rather than
// carried over into the imported config.
//
//nolint:cyclop // one branch per connection-string dialect
func kumaDatabaseConfig(
	monitor *kumaMonitor, timeout, name string, warn *warnings,
) (map[string]any, bool) {
	conn := strings.TrimSpace(monitor.DatabaseConnectionString)
	if conn == "" {
		warn.add(name, "databaseConnectionString", "monitor skipped: the connection string is empty")

		return nil, false
	}

	cfg := map[string]any{}

	if monitor.Type == "sqlserver" {
		if !kumaParseSQLServerConn(conn, cfg) {
			warn.add(name, "databaseConnectionString", "monitor skipped: the connection string could not be parsed")

			return nil, false
		}
	} else {
		parsed, err := url.Parse(conn)
		if err != nil || parsed.Hostname() == "" {
			warn.add(name, "databaseConnectionString", "monitor skipped: the connection string could not be parsed")

			return nil, false
		}

		cfg["host"] = parsed.Hostname()

		if portStr := parsed.Port(); portStr != "" {
			if port, convErr := strconv.Atoi(portStr); convErr == nil {
				cfg["port"] = port
			}
		}

		if parsed.User != nil && parsed.User.Username() != "" {
			cfg["username"] = parsed.User.Username()
		}

		if db := strings.TrimPrefix(parsed.Path, "/"); db != "" {
			if monitor.Type == "redis" {
				if idx, convErr := strconv.Atoi(db); convErr == nil {
					cfg["database"] = idx
				}
			} else {
				cfg["database"] = db
			}
		}
	}

	if _, hasPassword := passwordOf(conn); hasPassword {
		warn.add(name, "databaseConnectionString",
			"the database password was not imported — re-enter the credential on the check")
	}

	if monitor.DatabaseQuery != "" && monitor.Type != "redis" && monitor.Type != "mongodb" {
		cfg["query"] = monitor.DatabaseQuery
	} else if monitor.DatabaseQuery != "" {
		warn.add(name, "databaseQuery", "custom queries are not supported by the SolidPing %s checker", monitor.Type)
	}

	if timeout != "" {
		cfg["timeout"] = timeout
	}

	return cfg, true
}

// kumaParseSQLServerConn parses an ADO-style "Key=Value;" connection string.
func kumaParseSQLServerConn(conn string, cfg map[string]any) bool {
	for _, part := range strings.Split(conn, ";") {
		key, value, found := strings.Cut(part, "=")
		if !found {
			continue
		}

		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)

		switch key {
		case "server", "data source", "address":
			host, port := splitHostPortComma(value)
			cfg["host"] = host

			if port > 0 {
				cfg["port"] = port
			}
		case "database", "initial catalog":
			cfg["database"] = value
		case "user id", "uid", "user":
			cfg["username"] = value
		}
	}

	_, ok := cfg["host"]

	return ok
}

// passwordOf reports whether a connection string carries a password.
func passwordOf(conn string) (string, bool) {
	if parsed, err := url.Parse(conn); err == nil && parsed.User != nil {
		if pass, set := parsed.User.Password(); set && pass != "" {
			return pass, true
		}
	}

	for _, part := range strings.Split(conn, ";") {
		key, value, found := strings.Cut(part, "=")
		if !found {
			continue
		}

		if strings.EqualFold(strings.TrimSpace(key), "password") && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), true
		}
	}

	return "", false
}

// splitHostPort splits a "host:port" string; port is 0 when absent.
func splitHostPort(value string) (string, int) {
	host, portStr, found := strings.Cut(value, ":")
	if !found {
		return value, 0
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return host, 0
	}

	return host, port
}

// splitHostPortComma splits SQL Server's "host,port" form.
func splitHostPortComma(value string) (string, int) {
	host, portStr, found := strings.Cut(value, ",")
	if !found {
		return value, 0
	}

	port, err := strconv.Atoi(strings.TrimSpace(portStr))
	if err != nil {
		return host, 0
	}

	return strings.TrimSpace(host), port
}

// kumaJSONPath normalizes a Kuma jsonPath (jsonata-ish "status.code") into the
// JSONPath syntax the http checker expects.
func kumaJSONPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "$"
	}

	if strings.HasPrefix(path, "$") {
		return path
	}

	if strings.HasPrefix(path, ".") || strings.HasPrefix(path, "[") {
		return "$" + path
	}

	return "$." + path
}
