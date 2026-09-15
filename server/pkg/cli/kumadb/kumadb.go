// Package kumadb reads an Uptime Kuma SQLite database (kuma.db) and
// reconstructs the Uptime Kuma 1.x backup-JSON `monitorList` shape that
// importers.UptimeKumaConverter (server/internal/handlers/checks/importers)
// reads, without ever touching the tables that hold monitoring history or
// secrets: heartbeat, stat_minutely/hourly/daily, notification, user, api_key
// are never read. monitor_notification is read for its row count only, never
// its contents.
//
// This package deliberately duplicates the JSON field mapping
// importers.uptimekuma.go already knows, rather than importing that
// package's unexported kumaMonitor type: kumadb is linked into the `sp` CLI
// binary, which imports nothing under server/internal/handlers/* today, and
// staying a small, dependency-free leaf keeps it that way. The column → JSON
// key mapping below must be kept in sync with uptimekuma.go's kumaMonitor
// struct and with server/internal/handlers/checks/importers/mapping.md,
// which documents it as the source of truth; monitorColumns exists
// specifically so a test can cross-check it against a real database's
// PRAGMA table_info(monitor) and fail loudly on drift.
package kumadb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/sqlitedriver"
)

// sqliteHeaderMagic is the first 16 bytes of every SQLite database file.
const sqliteHeaderMagic = "SQLite format 3\x00"

// ErrNotSQLite is returned when the file at the given path does not carry a
// SQLite file header.
var ErrNotSQLite = errors.New("not a SQLite database")

// ErrNoMonitorTable is returned when the file opens fine as SQLite but has no
// `monitor` table — it is not an Uptime Kuma database.
var ErrNoMonitorTable = errors.New("no `monitor` table found — this does not look like an Uptime Kuma database")

// ErrNoDriver is returned when this build links no SQLite driver at all (an
// unsupported GOOS/GOARCH with cgo disabled). None of sp's shipped release
// targets hit this — see internal/db/sqlitedriver's platform list — but a
// from-source build on an exotic platform can.
var ErrNoDriver = errors.New("this build links no SQLite driver")

// backupVersion is the `version` field of the reconstructed backup document.
// UptimeKumaConverter never reads this field; it exists so the JSON is
// self-describing to a human inspecting it (e.g. via `sp checks import
// --from uptime-kuma-db --dry-run -f` piped to a file).
const backupVersion = "reconstructed by sp from kuma.db"

// backup is the 1.x backup-JSON shape importers.UptimeKumaConverter reads
// (its own kumaBackup, duplicated here — see the package doc).
type backup struct {
	Version     string    `json:"version"`
	MonitorList []monitor `json:"monitorList"`
}

// monitor mirrors one entry of Kuma's monitorList. Field-for-field, this is
// importers.kumaMonitor's JSON shape; see mapping.md for the column → key
// mapping this was built from.
//
//nolint:tagliatelle // field names are Uptime Kuma's, not ours
type monitor struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Active      *bool  `json:"active"`
	Parent      *int64 `json:"parent,omitempty"`

	URL      string `json:"url"`
	Hostname string `json:"hostname"`
	Port     *int64 `json:"port,omitempty"`
	Method   string `json:"method"`
	Body     string `json:"body"`
	Headers  string `json:"headers"`

	Interval      int64 `json:"interval"`
	RetryInterval int64 `json:"retryInterval"`
	MaxRetries    int64 `json:"maxretries"`
	Timeout       int64 `json:"timeout"`

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

	// NotificationIDList never carries real notification ids — the
	// `notification` table is never read, only monitor_notification's row
	// count. A single placeholder key is enough to make
	// UptimeKumaConverter's existing "notification bindings are not
	// imported" warning fire exactly when the monitor really has one or
	// more bindings (see convertMonitor in uptimekuma.go, which only checks
	// len(monitor.NotificationIDList) > 0).
	NotificationIDList map[string]bool `json:"notificationIDList,omitempty"`

	Tags []monitorTag `json:"tags,omitempty"`
}

// monitorTag mirrors one entry of a Kuma monitor's tags array.
type monitorTag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// monitorColumns is the exact, ordered column list this package selects off
// `monitor`. Spelled out once so TestReadColumnsMatchFixtureSchema can cross
// check it against PRAGMA table_info(monitor) on a real database and fail
// loudly the day a Kuma release renames one of them.
//
//nolint:gochecknoglobals // read-only column list, shared with the reader test on purpose
var monitorColumns = []string{
	"id", "name", "description", "type", "active", "parent",
	"url", "hostname", "port", "method", "body", "headers",
	"interval", "retry_interval", "maxretries", "timeout",
	"keyword", "invert_keyword", "json_path", "expected_value", "accepted_statuscodes_json",
	"ignore_tls", "upside_down",
	"dns_resolve_server", "dns_resolve_type",
	"docker_container",
	"mqtt_topic", "mqtt_username", "mqtt_password", "mqtt_success_message",
	"database_connection_string", "database_query",
	"grpc_url", "grpc_service_name", "grpc_enable_tls",
	"auth_method", "basic_auth_user", "basic_auth_pass",
}

// Read opens the Uptime Kuma SQLite database at path read-only and returns
// the reconstructed 1.x backup-JSON bytes, ready to POST to
// /checks/import/convert?source=uptime-kuma.
//
// path is opened with mode=ro, never immutable=1: Kuma runs in WAL mode, and
// immutable would let SQLite assume the file never changes and ignore a
// kuma.db-wal sitting next to it, silently dropping monitors created since
// Kuma's last WAL checkpoint.
func Read(ctx context.Context, path string) ([]byte, error) {
	if err := checkSQLiteHeader(path); err != nil {
		return nil, err
	}

	if sqlitedriver.Name == "" {
		return nil, ErrNoDriver
	}

	db, err := sql.Open(sqlitedriver.Name, fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", path))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer db.Close() //nolint:errcheck // best-effort close of a read-only handle

	return read(ctx, db)
}

// read runs the actual queries against an already-open handle — split out
// from Read so tests can exercise it against an in-memory / pre-populated
// *sql.DB without going through a real file.
func read(ctx context.Context, db *sql.DB) ([]byte, error) {
	if err := checkMonitorTable(ctx, db); err != nil {
		return nil, err
	}

	monitors, err := readMonitors(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("read monitor table: %w", err)
	}

	tagsByMonitor, err := readTags(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("read tag/monitor_tag: %w", err)
	}

	notified, err := readNotifiedMonitorIDs(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("read monitor_notification: %w", err)
	}

	for i := range monitors {
		monitors[i].Tags = tagsByMonitor[monitors[i].ID]

		if notified[monitors[i].ID] {
			monitors[i].NotificationIDList = map[string]bool{"1": true}
		}
	}

	encoded, err := json.Marshal(backup{Version: backupVersion, MonitorList: monitors})
	if err != nil {
		return nil, fmt.Errorf("encode monitorList: %w", err)
	}

	return encoded, nil
}

// checkSQLiteHeader reads the first 16 bytes of path and confirms they carry
// the SQLite file magic, without going through the SQL driver (which opens
// files lazily and would otherwise report a confusing error on the first
// query instead of a clear one up front).
func checkSQLiteHeader(path string) error {
	file, err := os.Open(path) // path is a CLI-supplied file argument, same as `sp checks import <file>`
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close() //nolint:errcheck // read-only, nothing to flush

	header := make([]byte, len(sqliteHeaderMagic))

	if _, err := file.Read(header); err != nil {
		return fmt.Errorf("%w: %s (could not read header: %w)", ErrNotSQLite, path, err)
	}

	if string(header) != sqliteHeaderMagic {
		return fmt.Errorf("%w: %s", ErrNotSQLite, path)
	}

	return nil
}

// checkMonitorTable confirms the database has a `monitor` table.
func checkMonitorTable(ctx context.Context, db *sql.DB) error {
	var name string

	query := `SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'monitor'`
	err := db.QueryRowContext(ctx, query).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNoMonitorTable
	}

	if err != nil {
		return fmt.Errorf("check for monitor table: %w", err)
	}

	return nil
}

// monitorRow holds one `monitor` row's columns with SQL-appropriate
// nullability, before it is translated into the JSON-facing monitor type.
type monitorRow struct {
	id                       int64
	name                     sql.NullString
	description              sql.NullString
	monitorType              sql.NullString
	active                   bool
	parent                   sql.NullInt64
	url                      sql.NullString
	hostname                 sql.NullString
	port                     sql.NullInt64
	method                   string
	body                     sql.NullString
	headers                  sql.NullString
	interval                 int64
	retryInterval            int64
	maxRetries               int64
	timeout                  float64
	keyword                  sql.NullString
	invertKeyword            bool
	jsonPath                 sql.NullString
	expectedValue            sql.NullString
	acceptedStatusCodesJSON  string
	ignoreTLS                bool
	upsideDown               bool
	dnsResolveServer         sql.NullString
	dnsResolveType           sql.NullString
	dockerContainer          sql.NullString
	mqttTopic                sql.NullString
	mqttUsername             sql.NullString
	mqttPassword             sql.NullString
	mqttSuccessMessage       sql.NullString
	databaseConnectionString sql.NullString
	databaseQuery            sql.NullString
	grpcURL                  sql.NullString
	grpcServiceName          sql.NullString
	grpcEnableTLS            bool
	authMethod               sql.NullString
	basicAuthUser            sql.NullString
	basicAuthPass            sql.NullString
}

// scanArgs returns the Scan() destinations in the same order as
// monitorColumns.
func (r *monitorRow) scanArgs() []any {
	return []any{
		&r.id, &r.name, &r.description, &r.monitorType, &r.active, &r.parent,
		&r.url, &r.hostname, &r.port, &r.method, &r.body, &r.headers,
		&r.interval, &r.retryInterval, &r.maxRetries, &r.timeout,
		&r.keyword, &r.invertKeyword, &r.jsonPath, &r.expectedValue, &r.acceptedStatusCodesJSON,
		&r.ignoreTLS, &r.upsideDown,
		&r.dnsResolveServer, &r.dnsResolveType,
		&r.dockerContainer,
		&r.mqttTopic, &r.mqttUsername, &r.mqttPassword, &r.mqttSuccessMessage,
		&r.databaseConnectionString, &r.databaseQuery,
		&r.grpcURL, &r.grpcServiceName, &r.grpcEnableTLS,
		&r.authMethod, &r.basicAuthUser, &r.basicAuthPass,
	}
}

// readMonitors reads every row of `monitor`, ordered by id so the output is
// deterministic (and matches the order a real Kuma backup export would list
// them, insofar as that also follows id order).
func readMonitors(ctx context.Context, db *sql.DB) ([]monitor, error) {
	query := "SELECT " + strings.Join(monitorColumns, ", ") + " FROM monitor ORDER BY id"

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query monitor: %w", err)
	}
	defer rows.Close() //nolint:errcheck // best effort on a read-only cursor

	monitors := make([]monitor, 0)

	for rows.Next() {
		var row monitorRow

		if err := rows.Scan(row.scanArgs()...); err != nil {
			return nil, fmt.Errorf("scan monitor row: %w", err)
		}

		monitors = append(monitors, row.toMonitor())
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate monitor rows: %w", err)
	}

	return monitors, nil
}

// toMonitor translates one SQL row into the JSON-facing shape.
func (r *monitorRow) toMonitor() monitor {
	active := r.active

	converted := monitor{
		ID:            r.id,
		Name:          r.name.String,
		Description:   r.description.String,
		Type:          r.monitorType.String,
		Active:        &active,
		URL:           r.url.String,
		Hostname:      r.hostname.String,
		Method:        r.method,
		Body:          r.body.String,
		Headers:       r.headers.String,
		Interval:      r.interval,
		RetryInterval: r.retryInterval,
		MaxRetries:    r.maxRetries,
		Timeout:       int64(r.timeout),
		Keyword:       r.keyword.String,
		InvertKeyword: r.invertKeyword,
		JSONPath:      r.jsonPath.String,
		ExpectedValue: r.expectedValue.String,

		IgnoreTLS:  r.ignoreTLS,
		UpsideDown: r.upsideDown,

		DNSResolveServer: r.dnsResolveServer.String,
		DNSResolveType:   r.dnsResolveType.String,

		DockerContainer: r.dockerContainer.String,

		MQTTTopic:          r.mqttTopic.String,
		MQTTUsername:       r.mqttUsername.String,
		MQTTPassword:       r.mqttPassword.String,
		MQTTSuccessMessage: r.mqttSuccessMessage.String,

		DatabaseConnectionString: r.databaseConnectionString.String,
		DatabaseQuery:            r.databaseQuery.String,

		GRPCUrl:         r.grpcURL.String,
		GRPCServiceName: r.grpcServiceName.String,
		GRPCEnableTLS:   r.grpcEnableTLS,

		AuthMethod:    r.authMethod.String,
		BasicAuthUser: r.basicAuthUser.String,
		BasicAuthPass: r.basicAuthPass.String,
	}

	if r.parent.Valid {
		converted.Parent = &r.parent.Int64
	}

	if r.port.Valid {
		converted.Port = &r.port.Int64
	}

	if codes, err := decodeAcceptedStatusCodes(r.acceptedStatusCodesJSON); err == nil {
		converted.AcceptedStatusCode = codes
	}

	return converted
}

// decodeAcceptedStatusCodes parses the accepted_statuscodes_json column
// (a JSON array of strings, e.g. `["200-299"]`) into the plain string slice
// the 1.x backup JSON carries under `accepted_statuscodes`.
func decodeAcceptedStatusCodes(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}

	var codes []string
	if err := json.Unmarshal([]byte(raw), &codes); err != nil {
		return nil, fmt.Errorf("parse accepted_statuscodes_json %q: %w", raw, err)
	}

	return codes, nil
}

// readTags reads tag ⋈ monitor_tag and groups the result by monitor id.
func readTags(ctx context.Context, db *sql.DB) (map[int64][]monitorTag, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT monitor_tag.monitor_id, tag.name, monitor_tag.value
		FROM monitor_tag
		JOIN tag ON tag.id = monitor_tag.tag_id
		ORDER BY monitor_tag.monitor_id, tag.name
	`)
	if err != nil {
		return nil, fmt.Errorf("query monitor_tag: %w", err)
	}
	defer rows.Close() //nolint:errcheck // best effort on a read-only cursor

	byMonitor := make(map[int64][]monitorTag)

	for rows.Next() {
		var (
			monitorID int64
			name      string
			value     sql.NullString
		)

		if err := rows.Scan(&monitorID, &name, &value); err != nil {
			return nil, fmt.Errorf("scan monitor_tag row: %w", err)
		}

		byMonitor[monitorID] = append(byMonitor[monitorID], monitorTag{Name: name, Value: value.String})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate monitor_tag rows: %w", err)
	}

	return byMonitor, nil
}

// readNotifiedMonitorIDs reads monitor_notification for the set of monitor
// ids that have at least one binding. The notification table itself — every
// Telegram token, Slack webhook, SMTP password it might hold — is never
// touched; only the junction table's monitor_id column is read.
func readNotifiedMonitorIDs(ctx context.Context, db *sql.DB) (map[int64]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT monitor_id FROM monitor_notification`)
	if err != nil {
		return nil, fmt.Errorf("query monitor_notification: %w", err)
	}
	defer rows.Close() //nolint:errcheck // best effort on a read-only cursor

	notified := make(map[int64]bool)

	for rows.Next() {
		var monitorID int64

		if err := rows.Scan(&monitorID); err != nil {
			return nil, fmt.Errorf("scan monitor_notification row: %w", err)
		}

		notified[monitorID] = true
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate monitor_notification rows: %w", err)
	}

	return notified, nil
}
