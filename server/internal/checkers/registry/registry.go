// Package registry provides factory functions for creating checkers and configs.
package registry

import (
	"github.com/fclairamb/solidping/server/internal/checkers/checkdns"
	"github.com/fclairamb/solidping/server/internal/checkers/checkdomain"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkftp"
	"github.com/fclairamb/solidping/server/internal/checkers/checkgrpc"
	"github.com/fclairamb/solidping/server/internal/checkers/checkheartbeat"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhttp"
	"github.com/fclairamb/solidping/server/internal/checkers/checkicmp"
	"github.com/fclairamb/solidping/server/internal/checkers/checkimap"
	"github.com/fclairamb/solidping/server/internal/checkers/checkjs"
	"github.com/fclairamb/solidping/server/internal/checkers/checkmongodb"
	"github.com/fclairamb/solidping/server/internal/checkers/checkmssql"
	"github.com/fclairamb/solidping/server/internal/checkers/checkmysql"
	"github.com/fclairamb/solidping/server/internal/checkers/checkoracle"
	"github.com/fclairamb/solidping/server/internal/checkers/checkpop3"
	"github.com/fclairamb/solidping/server/internal/checkers/checkpostgres"
	"github.com/fclairamb/solidping/server/internal/checkers/checkredis"
	"github.com/fclairamb/solidping/server/internal/checkers/checksftp"
	"github.com/fclairamb/solidping/server/internal/checkers/checksmtp"
	"github.com/fclairamb/solidping/server/internal/checkers/checkssh"
	"github.com/fclairamb/solidping/server/internal/checkers/checkssl"
	"github.com/fclairamb/solidping/server/internal/checkers/checktcp"
	"github.com/fclairamb/solidping/server/internal/checkers/checkudp"
	"github.com/fclairamb/solidping/server/internal/checkers/checkwebsocket"
	"github.com/fclairamb/solidping/server/internal/checkers/urlparse"
)

//nolint:gochecknoinits // Required to break import cycle between checkjs and registry
func init() {
	checkjs.ResolveChecker = func(checkType checkerdef.CheckType) (checkerdef.Checker, checkerdef.Config, bool) {
		checker, ok := GetChecker(checkType)
		if !ok {
			return nil, nil, false
		}

		cfg, ok := ParseConfig(checkType)
		if !ok {
			return nil, nil, false
		}

		return checker, cfg, true
	}
}

// GetChecker retrieves a checker by type.
// Returns the checker and true if found, nil and false otherwise.
//
//nolint:ireturn,cyclop,funlen // Registry pattern requires interface return and growing switch
func GetChecker(checkType checkerdef.CheckType) (checkerdef.Checker, bool) {
	switch checkType {
	case checkerdef.CheckTypeHTTP:
		return &checkhttp.HTTPChecker{}, true
	case checkerdef.CheckTypeICMP:
		return &checkicmp.ICMPChecker{}, true
	case checkerdef.CheckTypeDNS:
		return &checkdns.DNSChecker{}, true
	case checkerdef.CheckTypeTCP:
		return &checktcp.TCPChecker{}, true
	case checkerdef.CheckTypeHeartbeat:
		return &checkheartbeat.HeartbeatChecker{}, true
	case checkerdef.CheckTypeDomain:
		return &checkdomain.DomainChecker{}, true
	case checkerdef.CheckTypeSSL:
		return &checkssl.SSLChecker{}, true
	case checkerdef.CheckTypeSMTP:
		return &checksmtp.SMTPChecker{}, true
	case checkerdef.CheckTypeUDP:
		return &checkudp.UDPChecker{}, true
	case checkerdef.CheckTypeSSH:
		return &checkssh.SSHChecker{}, true
	case checkerdef.CheckTypePOP3:
		return &checkpop3.POP3Checker{}, true
	case checkerdef.CheckTypeIMAP:
		return &checkimap.IMAPChecker{}, true
	case checkerdef.CheckTypeWebSocket:
		return &checkwebsocket.WebSocketChecker{}, true
	case checkerdef.CheckTypePostgreSQL:
		return &checkpostgres.PostgreSQLChecker{}, true
	case checkerdef.CheckTypeFTP:
		return &checkftp.FTPChecker{}, true
	case checkerdef.CheckTypeSFTP:
		return &checksftp.SFTPChecker{}, true
	case checkerdef.CheckTypeJS:
		return &checkjs.JSChecker{}, true
	case checkerdef.CheckTypeMySQL:
		return &checkmysql.MySQLChecker{}, true
	case checkerdef.CheckTypeRedis:
		return &checkredis.RedisChecker{}, true
	case checkerdef.CheckTypeMongoDB:
		return &checkmongodb.MongoDBChecker{}, true
	case checkerdef.CheckTypeMSSQL:
		return &checkmssql.MSSQLChecker{}, true
	case checkerdef.CheckTypeOracle:
		return &checkoracle.OracleChecker{}, true
	case checkerdef.CheckTypeGRPC:
		return &checkgrpc.GRPCChecker{}, true
	default:
		return nil, false
	}
}

// ParseConfig creates the appropriate config struct for a given check type.
// Returns the config interface and true if the type is known, nil and false otherwise.
//
// TODO: Remove it
//
//nolint:ireturn,cyclop,funlen // Registry pattern requires interface return and growing switch
func ParseConfig(checkType checkerdef.CheckType) (checkerdef.Config, bool) {
	switch checkType {
	case checkerdef.CheckTypeHTTP:
		return &checkhttp.HTTPConfig{}, true
	case checkerdef.CheckTypeICMP:
		return &checkicmp.ICMPConfig{}, true
	case checkerdef.CheckTypeDNS:
		return &checkdns.DNSConfig{}, true
	case checkerdef.CheckTypeTCP:
		return &checktcp.TCPConfig{}, true
	case checkerdef.CheckTypeHeartbeat:
		return &checkheartbeat.HeartbeatConfig{}, true
	case checkerdef.CheckTypeDomain:
		return &checkdomain.DomainConfig{}, true
	case checkerdef.CheckTypeSSL:
		return &checkssl.SSLConfig{}, true
	case checkerdef.CheckTypeSMTP:
		return &checksmtp.SMTPConfig{}, true
	case checkerdef.CheckTypeUDP:
		return &checkudp.UDPConfig{}, true
	case checkerdef.CheckTypeSSH:
		return &checkssh.SSHConfig{}, true
	case checkerdef.CheckTypePOP3:
		return &checkpop3.POP3Config{}, true
	case checkerdef.CheckTypeIMAP:
		return &checkimap.IMAPConfig{}, true
	case checkerdef.CheckTypeWebSocket:
		return &checkwebsocket.WebSocketConfig{}, true
	case checkerdef.CheckTypePostgreSQL:
		return &checkpostgres.PostgreSQLConfig{}, true
	case checkerdef.CheckTypeFTP:
		return &checkftp.FTPConfig{}, true
	case checkerdef.CheckTypeSFTP:
		return &checksftp.SFTPConfig{}, true
	case checkerdef.CheckTypeJS:
		return &checkjs.JSConfig{}, true
	case checkerdef.CheckTypeMySQL:
		return &checkmysql.MySQLConfig{}, true
	case checkerdef.CheckTypeRedis:
		return &checkredis.RedisConfig{}, true
	case checkerdef.CheckTypeMongoDB:
		return &checkmongodb.MongoDBConfig{}, true
	case checkerdef.CheckTypeMSSQL:
		return &checkmssql.MSSQLConfig{}, true
	case checkerdef.CheckTypeOracle:
		return &checkoracle.OracleConfig{}, true
	case checkerdef.CheckTypeGRPC:
		return &checkgrpc.GRPCConfig{}, true
	default:
		return nil, false
	}
}

// InferCheckType returns the check type for a given URL.
// Returns empty CheckType if type cannot be inferred.
func InferCheckType(urlStr string) checkerdef.CheckType {
	return urlparse.InferCheckType(urlStr)
}

// InferCheckTypeFromConfig examines a config map and infers the check type.
// Returns empty CheckType if type cannot be inferred.
func InferCheckTypeFromConfig(config map[string]any) checkerdef.CheckType {
	if url, ok := config["url"].(string); ok && url != "" {
		return InferCheckType(url)
	}

	return ""
}
