package checkerdef

import "time"

// Status represents the outcome of a check execution.
type Status int

// Check status constants — values match models.ResultStatus.
const (
	StatusRunning Status = 2 // Check process started but not yet completed
	StatusUp      Status = 3 // Check succeeded
	StatusDown    Status = 4 // Check failed (target unreachable or unhealthy)
	StatusTimeout Status = 5 // Check timed out
	StatusError   Status = 6 // Internal error during check execution
)

// String returns the string representation of the status.
func (s Status) String() string {
	switch s {
	case StatusRunning:
		return "running"
	case StatusUp:
		return "up"
	case StatusDown:
		return "down"
	case StatusTimeout:
		return "timeout"
	case StatusError:
		return "error"
	default:
		return "unknown"
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
	// CheckTypeGRPC performs gRPC health checks.
	CheckTypeGRPC CheckType = "grpc"
	// CheckTypeKafka performs Kafka cluster health checks.
	CheckTypeKafka CheckType = "kafka"
	// CheckTypeMQTT performs MQTT broker health checks.
	CheckTypeMQTT CheckType = "mqtt"
	// CheckTypeGameServer performs game server A2S query health checks.
	CheckTypeGameServer CheckType = "gameserver"
	// CheckTypeRabbitMQ performs RabbitMQ health checks.
	CheckTypeRabbitMQ CheckType = "rabbitmq"
	// CheckTypeSNMP performs SNMP health checks.
	CheckTypeSNMP CheckType = "snmp"
	// CheckTypeDocker performs Docker container health checks.
	CheckTypeDocker CheckType = "docker"
	// CheckTypeBrowser performs headless Chrome browser health checks.
	CheckTypeBrowser CheckType = "browser"
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

	labelCatNetwork        = "category:network"
	labelCatSecurity       = "category:security"
	labelCatMail           = "category:mail"
	labelCatRemoteAccess   = "category:remote-access"
	labelCatDatabase       = "category:database"
	labelCatMessaging      = "category:messaging"
	labelCatInfrastructure = "category:infrastructure"
	labelCatOther          = "category:other"
)

// CheckTypeMeta holds metadata and labels for a check type.
type CheckTypeMeta struct {
	Type          CheckType     `json:"type"`
	Labels        []string      `json:"labels"`
	Description   string        `json:"description"`
	MinPeriod     time.Duration `json:"-"` // Minimum allowed check period (0 = use global default)
	MaxPeriod     time.Duration `json:"-"` // Maximum allowed check period (0 = no limit)
	DefaultPeriod time.Duration `json:"-"` // Default check period (0 = use global default)
}

// checkTypesRegistry is the authoritative registry of all check types with metadata.
//
//nolint:gochecknoglobals,lll // Registry is intentionally global; it's read-only after init.
var checkTypesRegistry = []CheckTypeMeta{
	{Type: CheckTypeHTTP, Labels: []string{labelSafe, labelStandalone, labelCatNetwork}, Description: "Monitor HTTP/HTTPS endpoints"},
	{Type: CheckTypeTCP, Labels: []string{labelSafe, labelStandalone, labelCatNetwork}, Description: "Check TCP port connectivity"},
	{Type: CheckTypeICMP, Labels: []string{labelUnsafe, labelReqRawSocket, labelCatNetwork}, Description: "Ping hosts via ICMP"},
	{Type: CheckTypeDNS, Labels: []string{labelSafe, labelStandalone, labelCatNetwork}, Description: "Monitor DNS resolution", DefaultPeriod: 5 * time.Minute},
	{Type: CheckTypeSSL, Labels: []string{labelSafe, labelStandalone, labelCatSecurity}, Description: "Check SSL certificate validity", MinPeriod: time.Hour, DefaultPeriod: 6 * time.Hour},
	{Type: CheckTypeDomain, Labels: []string{labelSafe, labelStandalone, labelCatSecurity}, Description: "Monitor domain expiration", MinPeriod: 6 * time.Hour, DefaultPeriod: 24 * time.Hour},
	{Type: CheckTypeHeartbeat, Labels: []string{labelSafe, labelStandalone, labelCatOther}, Description: "Receive heartbeat pings"},
	{Type: CheckTypeSMTP, Labels: []string{labelSafe, labelReqMailProtocol, labelCatMail}, Description: "Check SMTP server connectivity"},
	{Type: CheckTypeUDP, Labels: []string{labelSafe, labelStandalone, labelCatNetwork}, Description: "Check UDP port reachability"},
	{Type: CheckTypeSSH, Labels: []string{labelSafe, labelStandalone, labelCatRemoteAccess}, Description: "Check SSH server availability"},
	{Type: CheckTypePOP3, Labels: []string{labelSafe, labelReqMailProtocol, labelCatMail}, Description: "Check POP3 server availability"},
	{Type: CheckTypeIMAP, Labels: []string{labelSafe, labelReqMailProtocol, labelCatMail}, Description: "Check IMAP server availability"},
	{Type: CheckTypeWebSocket, Labels: []string{labelSafe, labelStandalone, labelCatNetwork}, Description: "Check WebSocket connectivity"},
	{Type: CheckTypePostgreSQL, Labels: []string{labelSafe, labelReqDatabaseDriver, labelCatDatabase}, Description: "Check PostgreSQL database health"},
	{Type: CheckTypeMySQL, Labels: []string{labelSafe, labelReqDatabaseDriver, labelCatDatabase}, Description: "Check MySQL/MariaDB database health"},
	{Type: CheckTypeRedis, Labels: []string{labelSafe, labelReqDatabaseDriver, labelCatDatabase}, Description: "Check Redis server health"},
	{Type: CheckTypeMongoDB, Labels: []string{labelSafe, labelReqDatabaseDriver, labelCatDatabase}, Description: "Check MongoDB database health"},
	{Type: CheckTypeFTP, Labels: []string{labelSafe, labelReqFileProtocol, labelCatRemoteAccess}, Description: "Check FTP server availability"},
	{Type: CheckTypeSFTP, Labels: []string{labelSafe, labelReqFileProtocol, labelCatRemoteAccess}, Description: "Check SFTP server availability"},
	{Type: CheckTypeJS, Labels: []string{labelUnsafe, labelReqScripting, labelCatOther}, Description: "Run custom JavaScript scripts"},
	{Type: CheckTypeMSSQL, Labels: []string{labelSafe, labelReqDatabaseDriver, labelCatDatabase}, Description: "Check Microsoft SQL Server health"},
	{Type: CheckTypeOracle, Labels: []string{labelSafe, labelReqDatabaseDriver, labelCatDatabase}, Description: "Check Oracle Database health"},
	{Type: CheckTypeGRPC, Labels: []string{labelSafe, labelStandalone, labelCatNetwork}, Description: "Check gRPC service health"},
	{Type: CheckTypeKafka, Labels: []string{labelSafe, labelReqMessagingClient, labelCatMessaging}, Description: "Check Kafka cluster health"},
	{Type: CheckTypeMQTT, Labels: []string{labelSafe, labelReqMessagingClient, labelCatMessaging}, Description: "Check MQTT broker connectivity"},
	{Type: CheckTypeGameServer, Labels: []string{labelSafe, labelStandalone, labelCatOther}, Description: "Monitor game server via A2S protocol"},
	{Type: CheckTypeRabbitMQ, Labels: []string{labelSafe, labelReqMessagingClient, labelCatMessaging}, Description: "Check RabbitMQ server health"},
	{Type: CheckTypeSNMP, Labels: []string{labelSafe, labelStandalone, labelCatInfrastructure}, Description: "Monitor devices via SNMP"},
	{Type: CheckTypeDocker, Labels: []string{labelUnsafe, labelReqDockerSocket, labelCatInfrastructure}, Description: "Monitor Docker container health"},
	{Type: CheckTypeBrowser, Labels: []string{labelUnsafe, labelReqChrome, labelCatOther}, Description: "Monitor pages with headless Chrome"},
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
		for _, have := range m.Labels {
			if want == have {
				return true
			}
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
		CheckTypeGRPC,
		CheckTypeKafka,
		CheckTypeMQTT,
		CheckTypeGameServer,
		CheckTypeRabbitMQ,
		CheckTypeSNMP,
		CheckTypeDocker,
		CheckTypeBrowser,
	}
}
