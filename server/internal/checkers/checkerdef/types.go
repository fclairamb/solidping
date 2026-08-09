package checkerdef

import (
	"slices"
	"time"
)

// Status represents the outcome of a check execution.
type Status int

// Check status constants — values match models.ResultStatus.
const (
	StatusRunning Status = 2 // Check process started but not yet completed
	StatusUp      Status = 3 // Check succeeded
	StatusDown    Status = 4 // Check failed (target unreachable or unhealthy)
	StatusTimeout Status = 5 // Check timed out
	StatusError   Status = 6 // Internal error during check execution
	// StatusDegraded is the aggregated rollup status: a window contained
	// warning(s) but no dominating failure. It is produced ONLY by the
	// aggregation job, never returned by a checker's Execute. It is declared
	// here so Severity() can rank the value the rollup writes.
	StatusDegraded Status = 7
	// StatusWarning indicates the target is up but there is something to
	// report (e.g. a certificate nearing expiry, a flapping container). It
	// counts as up for availability and is neutral for incidents; the
	// aggregation job promotes any window containing a raw Warning to the
	// aggregated Degraded status. Checkers never emit Degraded directly.
	StatusWarning Status = 8
)

// Status string labels.
const (
	statusStrRunning  = "running"
	statusStrUp       = "up"
	statusStrDown     = "down"
	statusStrTimeout  = "timeout"
	statusStrError    = "error"
	statusStrWarning  = "warning"
	statusStrDegraded = "degraded"
	statusStrUnknown  = "unknown"
)

// String returns the string representation of the status.
func (s Status) String() string {
	switch s {
	case StatusRunning:
		return statusStrRunning
	case StatusUp:
		return statusStrUp
	case StatusDown:
		return statusStrDown
	case StatusTimeout:
		return statusStrTimeout
	case StatusError:
		return statusStrError
	case StatusWarning:
		return statusStrWarning
	case StatusDegraded:
		return statusStrDegraded
	default:
		return statusStrUnknown
	}
}

// Severity ranks statuses so the aggregation job can resolve a dominant
// status by gravity rather than by raw numeric value (the numbers do not
// encode severity once Degraded=7 / Warning=8 exist). Higher = more severe.
//
// Hard failures (Down/Timeout/Error) outrank everything; the aggregated
// Degraded status (7, produced only by rollups) sits below failures but
// above Up; Warning ranks at Up level for availability (it counts as up),
// while its promotion to Degraded in a rollup is handled separately in the
// aggregation job, not here. Created/Running are lifecycle markers and rank
// lowest.
func (s Status) Severity() int {
	switch s {
	case StatusDown, StatusTimeout, StatusError:
		return 4
	case StatusDegraded:
		return 3
	case StatusUp, StatusWarning:
		return 2
	case StatusRunning:
		return 1
	default:
		return 0
	}
}

// Result represents the outcome of executing a check.
type Result struct {
	Status   Status         // The check status
	Duration time.Duration  // Time taken to execute the check
	Metrics  map[string]any // Numerical metrics that can be aggregated (e.g., ttfb, dns_time)
	Output   map[string]any // Diagnostic output (error messages, status text, etc.)
}

// CheckType represents the type of a check.
type CheckType string

// Supported check types.
const (
	// CheckTypeHTTP performs HTTP/HTTPS endpoint monitoring.
	CheckTypeHTTP CheckType = "http"
	// CheckTypeTCP performs TCP port connectivity checks.
	CheckTypeTCP CheckType = "tcp"
	// CheckTypeICMP performs ICMP ping checks.
	CheckTypeICMP CheckType = "icmp"
	// CheckTypeDNS performs DNS record resolution checks.
	CheckTypeDNS CheckType = "dns"
	// CheckTypeSSL performs SSL/TLS certificate validation checks.
	CheckTypeSSL CheckType = "ssl"
	// CheckTypeHeartbeat monitors via incoming pings (passive check).
	CheckTypeHeartbeat CheckType = "heartbeat"
	// CheckTypeEmail monitors via incoming emails to a unique address (passive check).
	CheckTypeEmail CheckType = "email"
	// CheckTypeDomain monitors domain name expiration.
	CheckTypeDomain CheckType = "domain"
	// CheckTypeSMTP performs SMTP server health checks.
	CheckTypeSMTP CheckType = "smtp"
	// CheckTypeUDP performs UDP port reachability checks.
	CheckTypeUDP CheckType = "udp"
	// CheckTypeSSH performs SSH server health checks.
	CheckTypeSSH CheckType = "ssh"
	// CheckTypePOP3 performs POP3 server health checks.
	CheckTypePOP3 CheckType = "pop3"
	// CheckTypeIMAP performs IMAP server health checks.
	CheckTypeIMAP CheckType = "imap"
	// CheckTypeWebSocket performs WebSocket connectivity checks.
	CheckTypeWebSocket CheckType = "websocket"
	// CheckTypePostgreSQL performs PostgreSQL database health checks.
	CheckTypePostgreSQL CheckType = "postgresql"
	// CheckTypeFTP performs FTP server health checks.
	CheckTypeFTP CheckType = "ftp"
	// CheckTypeSFTP performs SFTP server health checks.
	CheckTypeSFTP CheckType = "sftp"
	// CheckTypeJS runs custom JavaScript monitoring scripts.
	CheckTypeJS CheckType = "js"
	// CheckTypeMySQL performs MySQL/MariaDB database health checks.
	CheckTypeMySQL CheckType = "mysql"
	// CheckTypeRedis performs Redis health checks.
	CheckTypeRedis CheckType = "redis"
	// CheckTypeMongoDB performs MongoDB database health checks.
	CheckTypeMongoDB CheckType = "mongodb"
	// CheckTypeMSSQL performs Microsoft SQL Server health checks.
	CheckTypeMSSQL CheckType = "mssql"
	// CheckTypeOracle performs Oracle Database health checks.
	CheckTypeOracle CheckType = "oracle"
	// CheckTypeClickHouse performs ClickHouse health checks over the native
	// (binary) protocol.
	CheckTypeClickHouse CheckType = "clickhouse"
	// CheckTypeGRPC performs gRPC health checks.
	CheckTypeGRPC CheckType = "grpc"
	// CheckTypeKafka performs Kafka cluster health checks.
	CheckTypeKafka CheckType = "kafka"
	// CheckTypeMQTT performs MQTT broker health checks.
	CheckTypeMQTT CheckType = "mqtt"
	// CheckTypeA2S performs Source engine game server health checks via the A2S query protocol.
	CheckTypeA2S CheckType = "a2s"
	// CheckTypeMinecraft performs Minecraft server health checks (Java + Bedrock editions).
	CheckTypeMinecraft CheckType = "minecraft"
	// CheckTypeRabbitMQ performs RabbitMQ health checks.
	CheckTypeRabbitMQ CheckType = "rabbitmq"
	// CheckTypeSNMP performs SNMP health checks.
	CheckTypeSNMP CheckType = "snmp"
	// CheckTypeDocker performs Docker container health checks.
	CheckTypeDocker CheckType = "docker"
	// CheckTypeBrowser performs headless Chrome browser health checks.
	CheckTypeBrowser CheckType = "browser"
	// CheckTypeFreeboxLine monitors xDSL/FTTH line quality via the Freebox OS API.
	CheckTypeFreeboxLine CheckType = "freebox_line"
	// CheckTypeDNSBL checks whether an IP/domain is listed on DNS blocklists.
	CheckTypeDNSBL CheckType = "dnsbl"
	// CheckTypeSIP checks SIP server reachability (OPTIONS) and registration (REGISTER).
	CheckTypeSIP CheckType = "sip"
	// CheckTypeKubernetes monitors a Kubernetes workload's replica health
	// (Deployment / ReplicaSet ready vs desired replicas).
	CheckTypeKubernetes CheckType = "kubernetes"
	// CheckTypeNTP monitors an NTP time server: reachability plus the server's
	// self-reported health (stratum, leap indicator, root distance), with
	// optional clock-offset and max-stratum thresholds.
	CheckTypeNTP CheckType = "ntp"
	// CheckTypeRDP monitors Remote Desktop Protocol servers via the pre-auth
	// X.224 negotiation handshake (MS-RDPBCGR): service liveness, negotiated
	// security protocol (optionally enforcing NLA), and certificate expiry
	// when a TLS-based protocol is selected. No credentials are used.
	CheckTypeRDP CheckType = "rdp"
	// CheckTypeSleep is a synthetic/testing check that sleeps for a configured
	// duration. It performs no network I/O and exists as a deterministic load
	// generator for the scheduler. It is NOT a customer-facing check type and
	// must not be counted in the customer "N check types" tally.
	CheckTypeSleep CheckType = "sleep"
)

// Common output and config map keys used across checker implementations.
const (
	OutputKeyError      = "error"
	OutputKeyHost       = "host"
	OutputKeyPort       = "port"
	OutputKeyMethod     = "method"
	OutputKeyTimeout    = "timeout"
	OutputKeyCount      = "count"
	OutputKeyOID        = "oid"
	OutputKeyURL        = "url"
	OutputKeyStatusCode = "status_code"
	OutputKeyDurationMs = "duration_ms"
	OutputKeyDomain     = "domain"
	OutputKeyRecordType = "record_type"

	// OutputKeyTLSVerifySkipped marks a result whose request ran with TLS
	// certificate verification disabled (checkhttp's verifySsl: false), so
	// operators can see the reduced trust from the result details alone.
	OutputKeyTLSVerifySkipped = "tls_verify_skipped"
)

// Check type labels.
const (
	labelSafe   = "safe"
	labelUnsafe = "unsafe"

	labelStandalone = "standalone"

	labelReqRawSocket       = "requires:raw-socket"
	labelReqMailProtocol    = "requires:mail-protocol"
	labelReqDatabaseDriver  = "requires:database-driver"
	labelReqFileProtocol    = "requires:file-protocol"
	labelReqMessagingClient = "requires:messaging-client"
	labelReqScripting       = "requires:scripting-runtime"
	labelReqDockerSocket    = "requires:docker-socket"
	labelReqChrome          = "requires:chrome"
	labelReqK8sCluster      = "requires:kubernetes-cluster"

	labelCatNetwork        = "category:network"
	labelCatSecurity       = "category:security"
	labelCatMail           = "category:mail"
	labelCatRemoteAccess   = "category:remote-access"
	labelCatDatabase       = "category:database"
	labelCatMessaging      = "category:messaging"
	labelCatInfrastructure = "category:infrastructure"
	labelCatOther          = "category:other"
)

// GlobalMinPeriod is the smallest period the API accepts for any check type
// that does not declare its own (stricter) MinPeriod. It matches the smallest
// period in real-world use; sub-10s checks are out of scope for the
// results/aggregation model (spec 2026-07-01-04). The synthetic `sleep` type
// and internal checks are exempt at the validation site, not here.
const GlobalMinPeriod = 10 * time.Second

// CheckTypeMeta holds metadata and labels for a check type.
type CheckTypeMeta struct {
	Type          CheckType     `json:"type"`
	Labels        []string      `json:"labels"`
	Description   string        `json:"description"`
	MinPeriod     time.Duration `json:"-"` // Minimum allowed check period (0 = use global default)
	MaxPeriod     time.Duration `json:"-"` // Maximum allowed check period (0 = no limit)
	DefaultPeriod time.Duration `json:"-"` // Default check period (0 = use global default)

	// SupportsTunnel reports whether the type honors a tunnel dialer
	// (TunnelDialerFrom) and can therefore carry a `tunnelCheckUid` in its
	// config. Declarative metadata rather than a hand-maintained list
	// elsewhere: the API serves it and the dashboard gates its selector on it.
	// Enabled for every TCP-dialing type that routes its probe through the
	// context dialer: http, tcp, the mail protocols (smtp/imap/pop3), ssl, the
	// database drivers (postgres/mysql/mssql/oracle), and the client-library
	// types (redis/mongodb/rabbitmq/kafka/grpc/websocket/ftp/mqtt). UDP/ICMP
	// types cannot — SSH direct-tcpip forwards TCP only.
	SupportsTunnel bool `json:"supportsTunnel"`

	// SupportsIPVersion reports whether the type honors the shared `ipVersion`
	// config key (auto/ipv4/ipv6). Declarative metadata for the same reason as
	// SupportsTunnel: the API serves it and the dashboard gates its selector on
	// it, instead of a hand-maintained type list that would drift.
	//
	// Enabled for the types that resolve a hostname and pick one address
	// themselves (tcp, udp, icmp, ssl, ssh, smtp, imap, pop3, dnsbl) plus http,
	// which pins the family on its transport instead. Deliberately NOT enabled
	// for `dns`: for a DNS check "ipVersion" could mean either which record
	// types to assert on or which transport to reach the nameserver over —
	// different features, neither implemented here, so the option is rejected
	// rather than silently ignored. Everything else either has no network
	// target (heartbeat, email, sleep) or dials by name through a client
	// library that exposes no address-family seam.
	SupportsIPVersion bool `json:"supportsIpVersion"`
}

// checkTypesRegistry is the authoritative registry of all check types with metadata.
//
//nolint:gochecknoglobals,lll // Registry is intentionally global; it's read-only after init.
var checkTypesRegistry = []CheckTypeMeta{
	{Type: CheckTypeHTTP, Labels: []string{labelSafe, labelStandalone, labelCatNetwork}, Description: "Monitor HTTP/HTTPS endpoints", SupportsTunnel: true, SupportsIPVersion: true},
	{Type: CheckTypeTCP, Labels: []string{labelSafe, labelStandalone, labelCatNetwork}, Description: "Check TCP port connectivity", SupportsTunnel: true, SupportsIPVersion: true},
	{Type: CheckTypeICMP, Labels: []string{labelUnsafe, labelReqRawSocket, labelCatNetwork}, Description: "Ping hosts via ICMP", SupportsIPVersion: true},
	{Type: CheckTypeDNS, Labels: []string{labelSafe, labelStandalone, labelCatNetwork}, Description: "Monitor DNS resolution", DefaultPeriod: 5 * time.Minute},
	{Type: CheckTypeSSL, Labels: []string{labelSafe, labelStandalone, labelCatSecurity}, Description: "Check SSL certificate validity", MinPeriod: time.Hour, DefaultPeriod: 6 * time.Hour, SupportsTunnel: true, SupportsIPVersion: true},
	{Type: CheckTypeDomain, Labels: []string{labelSafe, labelStandalone, labelCatSecurity}, Description: "Monitor domain expiration", MinPeriod: 6 * time.Hour, DefaultPeriod: 24 * time.Hour},
	{Type: CheckTypeHeartbeat, Labels: []string{labelSafe, labelStandalone, labelCatOther}, Description: "Passive HTTP check"},
	{Type: CheckTypeEmail, Labels: []string{labelSafe, labelStandalone, labelCatOther}, Description: "Email reception (passive)"},
	{Type: CheckTypeSMTP, Labels: []string{labelSafe, labelReqMailProtocol, labelCatMail}, Description: "Check SMTP server connectivity", SupportsTunnel: true, SupportsIPVersion: true},
	{Type: CheckTypeUDP, Labels: []string{labelSafe, labelStandalone, labelCatNetwork}, Description: "Check UDP port reachability", SupportsIPVersion: true},
	{Type: CheckTypeSSH, Labels: []string{labelSafe, labelStandalone, labelCatRemoteAccess}, Description: "Check SSH server availability", SupportsIPVersion: true},
	{Type: CheckTypePOP3, Labels: []string{labelSafe, labelReqMailProtocol, labelCatMail}, Description: "Check POP3 server availability", SupportsTunnel: true, SupportsIPVersion: true},
	{Type: CheckTypeIMAP, Labels: []string{labelSafe, labelReqMailProtocol, labelCatMail}, Description: "Check IMAP server availability", SupportsTunnel: true, SupportsIPVersion: true},
	{Type: CheckTypeWebSocket, Labels: []string{labelSafe, labelStandalone, labelCatNetwork}, Description: "Check WebSocket connectivity", SupportsTunnel: true},
	{Type: CheckTypePostgreSQL, Labels: []string{labelSafe, labelReqDatabaseDriver, labelCatDatabase}, Description: "Check PostgreSQL database health", SupportsTunnel: true},
	{Type: CheckTypeMySQL, Labels: []string{labelSafe, labelReqDatabaseDriver, labelCatDatabase}, Description: "Check MySQL/MariaDB database health", SupportsTunnel: true},
	{Type: CheckTypeRedis, Labels: []string{labelSafe, labelReqDatabaseDriver, labelCatDatabase}, Description: "Check Redis server health", SupportsTunnel: true},
	{Type: CheckTypeMongoDB, Labels: []string{labelSafe, labelReqDatabaseDriver, labelCatDatabase}, Description: "Check MongoDB database health", SupportsTunnel: true},
	{Type: CheckTypeFTP, Labels: []string{labelSafe, labelReqFileProtocol, labelCatRemoteAccess}, Description: "Check FTP server availability", SupportsTunnel: true},
	{Type: CheckTypeSFTP, Labels: []string{labelSafe, labelReqFileProtocol, labelCatRemoteAccess}, Description: "Check SFTP server availability"},
	{Type: CheckTypeJS, Labels: []string{labelUnsafe, labelReqScripting, labelCatOther}, Description: "Run custom JavaScript scripts", MinPeriod: 30 * time.Second, DefaultPeriod: time.Minute},
	{Type: CheckTypeMSSQL, Labels: []string{labelSafe, labelReqDatabaseDriver, labelCatDatabase}, Description: "Check Microsoft SQL Server health", SupportsTunnel: true},
	{Type: CheckTypeOracle, Labels: []string{labelSafe, labelReqDatabaseDriver, labelCatDatabase}, Description: "Check Oracle Database health", SupportsTunnel: true},
	{Type: CheckTypeClickHouse, Labels: []string{labelSafe, labelReqDatabaseDriver, labelCatDatabase}, Description: "Check ClickHouse database health", SupportsTunnel: true},
	{Type: CheckTypeGRPC, Labels: []string{labelSafe, labelStandalone, labelCatNetwork}, Description: "Check gRPC service health", SupportsTunnel: true},
	{Type: CheckTypeKafka, Labels: []string{labelSafe, labelReqMessagingClient, labelCatMessaging}, Description: "Check Kafka cluster health", SupportsTunnel: true},
	{Type: CheckTypeMQTT, Labels: []string{labelSafe, labelReqMessagingClient, labelCatMessaging}, Description: "Check MQTT broker connectivity", SupportsTunnel: true},
	{Type: CheckTypeA2S, Labels: []string{labelSafe, labelStandalone, labelCatOther}, Description: "Monitor Source engine game servers via the A2S query protocol"},
	{Type: CheckTypeMinecraft, Labels: []string{labelSafe, labelStandalone, labelCatOther}, Description: "Monitor Minecraft servers (Java + Bedrock)"},
	{Type: CheckTypeRabbitMQ, Labels: []string{labelSafe, labelReqMessagingClient, labelCatMessaging}, Description: "Check RabbitMQ server health", SupportsTunnel: true},
	{Type: CheckTypeSNMP, Labels: []string{labelSafe, labelStandalone, labelCatInfrastructure}, Description: "Monitor devices via SNMP"},
	{Type: CheckTypeDocker, Labels: []string{labelUnsafe, labelReqDockerSocket, labelCatInfrastructure}, Description: "Monitor Docker container health"},
	{Type: CheckTypeBrowser, Labels: []string{labelUnsafe, labelReqChrome, labelCatOther}, Description: "Monitor pages with headless Chrome", MinPeriod: time.Minute, DefaultPeriod: 5 * time.Minute},
	{Type: CheckTypeFreeboxLine, Labels: []string{labelSafe, labelStandalone, labelCatInfrastructure}, Description: "Monitor Freebox xDSL/FTTH line quality", DefaultPeriod: 5 * time.Minute},
	{Type: CheckTypeDNSBL, Labels: []string{labelSafe, labelStandalone, labelCatSecurity}, Description: "Check if an IP/domain is on DNS blocklists", MinPeriod: 15 * time.Minute, DefaultPeriod: time.Hour, SupportsIPVersion: true},
	{Type: CheckTypeSIP, Labels: []string{labelSafe, labelStandalone, labelCatNetwork}, Description: "Check SIP server reachability and registration"},
	{Type: CheckTypeKubernetes, Labels: []string{labelSafe, labelReqK8sCluster, labelCatInfrastructure}, Description: "Monitor Kubernetes workload replica health"},
	{Type: CheckTypeNTP, Labels: []string{labelSafe, labelStandalone, labelCatNetwork}, Description: "Monitor NTP time servers", DefaultPeriod: 5 * time.Minute},
	{Type: CheckTypeRDP, Labels: []string{labelSafe, labelStandalone, labelCatNetwork}, Description: "Monitor RDP (Remote Desktop) servers"},
	{Type: CheckTypeSleep, Labels: []string{labelSafe, labelStandalone, labelCatOther}, Description: "Sleep for a fixed duration (synthetic/testing)", DefaultPeriod: 1 * time.Minute},
}

// GetCheckTypeMeta returns the metadata for a given check type, or nil if not found.
func GetCheckTypeMeta(ct CheckType) *CheckTypeMeta {
	for i := range checkTypesRegistry {
		if checkTypesRegistry[i].Type == ct {
			return &checkTypesRegistry[i]
		}
	}

	return nil
}

// ListCheckTypeMetas returns all registered check type metadata.
func ListCheckTypeMetas() []CheckTypeMeta {
	result := make([]CheckTypeMeta, len(checkTypesRegistry))
	copy(result, checkTypesRegistry)

	return result
}

// MatchesLabels returns true if the check type has any of the given labels.
func (m *CheckTypeMeta) MatchesLabels(labels []string) bool {
	for _, want := range labels {
		if slices.Contains(m.Labels, want) {
			return true
		}
	}

	return false
}

// ListSampleOptionType represents the type of sample configuration to retrieve.
type ListSampleOptionType uint8

// Sample option types.
const (
	// Default represents standard sample configurations for normal operation.
	Default ListSampleOptionType = iota
	// Demo represents sample configurations optimized for demonstration purposes.
	Demo ListSampleOptionType = iota
	// Test represents sample configurations for testing scenarios.
	Test ListSampleOptionType = iota
)

// ListSampleOptions represents options for listing check types.
type ListSampleOptions struct {
	Type    ListSampleOptionType
	BaseURL string // Base URL for self-referencing checks (e.g., fake API)
}

// ListCheckTypes returns a list of supported check types based on the provided options.
func ListCheckTypes(_ *ListSampleOptions) []CheckType {
	return []CheckType{
		CheckTypeHTTP,
		CheckTypeTCP,
		CheckTypeICMP,
		CheckTypeDNS,
		CheckTypeHeartbeat,
		CheckTypeEmail,
		CheckTypeDomain,
		CheckTypeSSL,
		CheckTypeSMTP,
		CheckTypeUDP,
		CheckTypeSSH,
		CheckTypePOP3,
		CheckTypeIMAP,
		CheckTypeWebSocket,
		CheckTypePostgreSQL,
		CheckTypeFTP,
		CheckTypeSFTP,
		CheckTypeJS,
		CheckTypeMySQL,
		CheckTypeRedis,
		CheckTypeMongoDB,
		CheckTypeMSSQL,
		CheckTypeOracle,
		CheckTypeClickHouse,
		CheckTypeGRPC,
		CheckTypeKafka,
		CheckTypeMQTT,
		CheckTypeA2S,
		CheckTypeMinecraft,
		CheckTypeRabbitMQ,
		CheckTypeSNMP,
		CheckTypeDocker,
		CheckTypeBrowser,
		CheckTypeFreeboxLine,
		CheckTypeDNSBL,
		CheckTypeSIP,
		CheckTypeKubernetes,
		CheckTypeNTP,
		CheckTypeRDP,
		CheckTypeSleep,
	}
}
