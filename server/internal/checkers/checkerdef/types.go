package checkerdef

import "time"

// Status represents the outcome of a check execution.
type Status int

// Check status constants.
const (
	StatusInitial Status = 0 // Check status unknown
	StatusUp      Status = 1 // Check succeeded
	StatusDown    Status = 2 // Check failed (target unreachable or unhealthy)
	StatusTimeout Status = 3 // Check timed out
	StatusError   Status = 4 // Internal error during check execution
	StatusRunning Status = 5 // Check process started but not yet completed
)

// String returns the string representation of the status.
func (s Status) String() string {
	switch s {
	case StatusInitial:
		return "initial"
	case StatusUp:
		return "up"
	case StatusDown:
		return "down"
	case StatusTimeout:
		return "timeout"
	case StatusError:
		return "error"
	case StatusRunning:
		return "running"
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
	// CheckTypeGameServer performs game server A2S query health checks.
	CheckTypeGameServer CheckType = "gameserver"
)

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
		CheckTypeGameServer,
	}
}
