// Package registry provides factory functions for creating checkers and configs.
package registry

import (
	"github.com/fclairamb/solidping/server/internal/checkers/checka2s"
	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser"
	"github.com/fclairamb/solidping/server/internal/checkers/checkclickhouse"
	"github.com/fclairamb/solidping/server/internal/checkers/checkdns"
	"github.com/fclairamb/solidping/server/internal/checkers/checkdnsbl"
	"github.com/fclairamb/solidping/server/internal/checkers/checkdocker"
	"github.com/fclairamb/solidping/server/internal/checkers/checkdomain"
	"github.com/fclairamb/solidping/server/internal/checkers/checkemail"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkfreeboxline"
	"github.com/fclairamb/solidping/server/internal/checkers/checkftp"
	"github.com/fclairamb/solidping/server/internal/checkers/checkgrpc"
	"github.com/fclairamb/solidping/server/internal/checkers/checkheartbeat"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhttp"
	"github.com/fclairamb/solidping/server/internal/checkers/checkicmp"
	"github.com/fclairamb/solidping/server/internal/checkers/checkimap"
	"github.com/fclairamb/solidping/server/internal/checkers/checkjs"
	"github.com/fclairamb/solidping/server/internal/checkers/checkkafka"
	"github.com/fclairamb/solidping/server/internal/checkers/checkkubernetes"
	"github.com/fclairamb/solidping/server/internal/checkers/checkminecraft"
	"github.com/fclairamb/solidping/server/internal/checkers/checkmongodb"
	"github.com/fclairamb/solidping/server/internal/checkers/checkmqtt"
	"github.com/fclairamb/solidping/server/internal/checkers/checkmssql"
	"github.com/fclairamb/solidping/server/internal/checkers/checkmysql"
	"github.com/fclairamb/solidping/server/internal/checkers/checkntp"
	"github.com/fclairamb/solidping/server/internal/checkers/checkoracle"
	"github.com/fclairamb/solidping/server/internal/checkers/checkpop3"
	"github.com/fclairamb/solidping/server/internal/checkers/checkpostgres"
	"github.com/fclairamb/solidping/server/internal/checkers/checkprometheus"
	"github.com/fclairamb/solidping/server/internal/checkers/checkrabbitmq"
	"github.com/fclairamb/solidping/server/internal/checkers/checkrdp"
	"github.com/fclairamb/solidping/server/internal/checkers/checkredis"
	"github.com/fclairamb/solidping/server/internal/checkers/checksftp"
	"github.com/fclairamb/solidping/server/internal/checkers/checksip"
	"github.com/fclairamb/solidping/server/internal/checkers/checksleep"
	"github.com/fclairamb/solidping/server/internal/checkers/checksmtp"
	"github.com/fclairamb/solidping/server/internal/checkers/checksnmp"
	"github.com/fclairamb/solidping/server/internal/checkers/checkssh"
	"github.com/fclairamb/solidping/server/internal/checkers/checkssl"
	"github.com/fclairamb/solidping/server/internal/checkers/checktcp"
	"github.com/fclairamb/solidping/server/internal/checkers/checkudp"
	"github.com/fclairamb/solidping/server/internal/checkers/checkwebsocket"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
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
	case checkerdef.CheckTypeEmail:
		return &checkemail.EmailChecker{}, true
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
	case checkerdef.CheckTypeClickHouse:
		return &checkclickhouse.ClickHouseChecker{}, true
	case checkerdef.CheckTypeGRPC:
		return &checkgrpc.GRPCChecker{}, true
	case checkerdef.CheckTypeKafka:
		return &checkkafka.KafkaChecker{}, true
	case checkerdef.CheckTypeMQTT:
		return &checkmqtt.MQTTChecker{}, true
	case checkerdef.CheckTypeA2S:
		return &checka2s.A2SChecker{}, true
	case checkerdef.CheckTypeMinecraft:
		return &checkminecraft.MinecraftChecker{}, true
	case checkerdef.CheckTypeRabbitMQ:
		return &checkrabbitmq.RabbitMQChecker{}, true
	case checkerdef.CheckTypeSNMP:
		return &checksnmp.SNMPChecker{}, true
	case checkerdef.CheckTypeDocker:
		return &checkdocker.DockerChecker{}, true
	case checkerdef.CheckTypeBrowser:
		return &checkbrowser.BrowserChecker{}, true
	case checkerdef.CheckTypeFreeboxLine:
		return &checkfreeboxline.FreeboxLineChecker{}, true
	case checkerdef.CheckTypeDNSBL:
		return &checkdnsbl.DNSBLChecker{}, true
	case checkerdef.CheckTypeSIP:
		return &checksip.SIPChecker{}, true
	case checkerdef.CheckTypeKubernetes:
		return &checkkubernetes.KubernetesChecker{}, true
	case checkerdef.CheckTypeNTP:
		return &checkntp.NTPChecker{}, true
	case checkerdef.CheckTypePrometheus:
		return &checkprometheus.PrometheusChecker{}, true
	case checkerdef.CheckTypeRDP:
		return &checkrdp.RDPChecker{}, true
	case checkerdef.CheckTypeSleep:
		return &checksleep.SleepChecker{}, true
	default:
		return nil, false
	}
}

// ParseConfig creates the appropriate config struct for a given check type.
// Returns the config interface and true if the type is known, nil and false otherwise.
//
// It delegates to configregistry, the light twin that knows only the configs.
// That is deliberate: there is ONE switch mapping a type to its config, so the
// offline validator in `sp` and the server can never disagree about what a
// check type accepts. Adding a checker means adding it there and to GetChecker
// below; TestRegistriesAgree fails if only one of the two is done.
//
// TODO: Remove it
//
//nolint:ireturn // Registry pattern requires interface return
func ParseConfig(checkType checkerdef.CheckType) (checkerdef.Config, bool) {
	return configregistry.ParseConfig(checkType)
}

// GetAllSampleConfigs collects sample configurations from all checker types that implement CheckerSamplesProvider.
func GetAllSampleConfigs(opts *checkerdef.ListSampleOptions) map[checkerdef.CheckType][]checkerdef.CheckSpec {
	result := make(map[checkerdef.CheckType][]checkerdef.CheckSpec)

	for _, checkType := range checkerdef.ListCheckTypes(opts) {
		checker, ok := GetChecker(checkType)
		if !ok {
			continue
		}

		provider, ok := checker.(checkerdef.CheckerSamplesProvider)
		if !ok {
			continue
		}

		samples := provider.GetSampleConfigs(opts)
		if len(samples) > 0 {
			result[checkType] = samples
		}
	}

	return result
}

// InferCheckType returns the check type for a given URL.
// Returns empty CheckType if type cannot be inferred.
func InferCheckType(urlStr string) checkerdef.CheckType {
	return configregistry.InferCheckType(urlStr)
}

// InferCheckTypeFromConfig examines a config map and infers the check type.
// Returns empty CheckType if type cannot be inferred.
func InferCheckTypeFromConfig(config map[string]any) checkerdef.CheckType {
	return configregistry.InferCheckTypeFromConfig(config)
}
