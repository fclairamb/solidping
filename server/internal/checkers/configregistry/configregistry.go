// Package configregistry maps a check type to its CONFIG only.
//
// It is the same closed switch as internal/checkers/registry, minus the
// execution half: it imports each checker's light `check<type>/config`
// sub-package and never the checker itself, so nothing here drags in
// client-go, go-ora, sarama, chromedp, grpc or a database driver. That is what
// lets `sp checks validate` parse and validate a config-as-code manifest
// offline, using the very same validators the server runs, in a binary a
// quarter the size.
//
// The heavy registry delegates its own ParseConfig here, so the two can never
// disagree about which types exist or what their configs are — one switch, not
// two. TestRegistriesAgree pins the rest.
package configregistry

import (
	a2sconfig "github.com/fclairamb/solidping/server/internal/checkers/checka2s/config"
	browserconfig "github.com/fclairamb/solidping/server/internal/checkers/checkbrowser/config"
	clickhouseconfig "github.com/fclairamb/solidping/server/internal/checkers/checkclickhouse/config"
	dnsconfig "github.com/fclairamb/solidping/server/internal/checkers/checkdns/config"
	dnsblconfig "github.com/fclairamb/solidping/server/internal/checkers/checkdnsbl/config"
	dockerconfig "github.com/fclairamb/solidping/server/internal/checkers/checkdocker/config"
	domainconfig "github.com/fclairamb/solidping/server/internal/checkers/checkdomain/config"
	emailconfig "github.com/fclairamb/solidping/server/internal/checkers/checkemail/config"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	freeboxlineconfig "github.com/fclairamb/solidping/server/internal/checkers/checkfreeboxline/config"
	ftpconfig "github.com/fclairamb/solidping/server/internal/checkers/checkftp/config"
	grpcconfig "github.com/fclairamb/solidping/server/internal/checkers/checkgrpc/config"
	heartbeatconfig "github.com/fclairamb/solidping/server/internal/checkers/checkheartbeat/config"
	httpconfig "github.com/fclairamb/solidping/server/internal/checkers/checkhttp/config"
	icmpconfig "github.com/fclairamb/solidping/server/internal/checkers/checkicmp/config"
	imapconfig "github.com/fclairamb/solidping/server/internal/checkers/checkimap/config"
	jsconfig "github.com/fclairamb/solidping/server/internal/checkers/checkjs/config"
	kafkaconfig "github.com/fclairamb/solidping/server/internal/checkers/checkkafka/config"
	kubernetesconfig "github.com/fclairamb/solidping/server/internal/checkers/checkkubernetes/config"
	minecraftconfig "github.com/fclairamb/solidping/server/internal/checkers/checkminecraft/config"
	mongodbconfig "github.com/fclairamb/solidping/server/internal/checkers/checkmongodb/config"
	mqttconfig "github.com/fclairamb/solidping/server/internal/checkers/checkmqtt/config"
	mssqlconfig "github.com/fclairamb/solidping/server/internal/checkers/checkmssql/config"
	mysqlconfig "github.com/fclairamb/solidping/server/internal/checkers/checkmysql/config"
	ntpconfig "github.com/fclairamb/solidping/server/internal/checkers/checkntp/config"
	oracleconfig "github.com/fclairamb/solidping/server/internal/checkers/checkoracle/config"
	pop3config "github.com/fclairamb/solidping/server/internal/checkers/checkpop3/config"
	postgresconfig "github.com/fclairamb/solidping/server/internal/checkers/checkpostgres/config"
	prometheusconfig "github.com/fclairamb/solidping/server/internal/checkers/checkprometheus/config"
	rabbitmqconfig "github.com/fclairamb/solidping/server/internal/checkers/checkrabbitmq/config"
	rdpconfig "github.com/fclairamb/solidping/server/internal/checkers/checkrdp/config"
	redisconfig "github.com/fclairamb/solidping/server/internal/checkers/checkredis/config"
	sftpconfig "github.com/fclairamb/solidping/server/internal/checkers/checksftp/config"
	sipconfig "github.com/fclairamb/solidping/server/internal/checkers/checksip/config"
	sleepconfig "github.com/fclairamb/solidping/server/internal/checkers/checksleep/config"
	smtpconfig "github.com/fclairamb/solidping/server/internal/checkers/checksmtp/config"
	snmpconfig "github.com/fclairamb/solidping/server/internal/checkers/checksnmp/config"
	sshconfig "github.com/fclairamb/solidping/server/internal/checkers/checkssh/config"
	sslconfig "github.com/fclairamb/solidping/server/internal/checkers/checkssl/config"
	tcpconfig "github.com/fclairamb/solidping/server/internal/checkers/checktcp/config"
	udpconfig "github.com/fclairamb/solidping/server/internal/checkers/checkudp/config"
	websocketconfig "github.com/fclairamb/solidping/server/internal/checkers/checkwebsocket/config"
	"github.com/fclairamb/solidping/server/internal/checkers/urlparse"
)

// ParseConfig returns a zero-value config for a check type, and true when the
// type is known to this build.
//
//nolint:ireturn,cyclop,funlen // Registry pattern requires interface return and growing switch
func ParseConfig(checkType checkerdef.CheckType) (checkerdef.Config, bool) {
	switch checkType {
	case checkerdef.CheckTypeHTTP:
		return &httpconfig.HTTPConfig{}, true
	case checkerdef.CheckTypeICMP:
		return &icmpconfig.ICMPConfig{}, true
	case checkerdef.CheckTypeDNS:
		return &dnsconfig.DNSConfig{}, true
	case checkerdef.CheckTypeTCP:
		return &tcpconfig.TCPConfig{}, true
	case checkerdef.CheckTypeHeartbeat:
		return &heartbeatconfig.HeartbeatConfig{}, true
	case checkerdef.CheckTypeEmail:
		return &emailconfig.EmailConfig{}, true
	case checkerdef.CheckTypeDomain:
		return &domainconfig.DomainConfig{}, true
	case checkerdef.CheckTypeSSL:
		return &sslconfig.SSLConfig{}, true
	case checkerdef.CheckTypeSMTP:
		return &smtpconfig.SMTPConfig{}, true
	case checkerdef.CheckTypeUDP:
		return &udpconfig.UDPConfig{}, true
	case checkerdef.CheckTypeSSH:
		return &sshconfig.SSHConfig{}, true
	case checkerdef.CheckTypePOP3:
		return &pop3config.POP3Config{}, true
	case checkerdef.CheckTypeIMAP:
		return &imapconfig.IMAPConfig{}, true
	case checkerdef.CheckTypeWebSocket:
		return &websocketconfig.WebSocketConfig{}, true
	case checkerdef.CheckTypePostgreSQL:
		return &postgresconfig.PostgreSQLConfig{}, true
	case checkerdef.CheckTypeFTP:
		return &ftpconfig.FTPConfig{}, true
	case checkerdef.CheckTypeSFTP:
		return &sftpconfig.SFTPConfig{}, true
	case checkerdef.CheckTypeJS:
		return &jsconfig.JSConfig{}, true
	case checkerdef.CheckTypeMySQL:
		return &mysqlconfig.MySQLConfig{}, true
	case checkerdef.CheckTypeRedis:
		return &redisconfig.RedisConfig{}, true
	case checkerdef.CheckTypeMongoDB:
		return &mongodbconfig.MongoDBConfig{}, true
	case checkerdef.CheckTypeMSSQL:
		return &mssqlconfig.MSSQLConfig{}, true
	case checkerdef.CheckTypeOracle:
		return &oracleconfig.OracleConfig{}, true
	case checkerdef.CheckTypeClickHouse:
		return &clickhouseconfig.ClickHouseConfig{}, true
	case checkerdef.CheckTypeGRPC:
		return &grpcconfig.GRPCConfig{}, true
	case checkerdef.CheckTypeKafka:
		return &kafkaconfig.KafkaConfig{}, true
	case checkerdef.CheckTypeMQTT:
		return &mqttconfig.MQTTConfig{}, true
	case checkerdef.CheckTypeA2S:
		return &a2sconfig.A2SConfig{}, true
	case checkerdef.CheckTypeMinecraft:
		return &minecraftconfig.MinecraftConfig{}, true
	case checkerdef.CheckTypeRabbitMQ:
		return &rabbitmqconfig.RabbitMQConfig{}, true
	case checkerdef.CheckTypeSNMP:
		return &snmpconfig.SNMPConfig{}, true
	case checkerdef.CheckTypeDocker:
		return &dockerconfig.DockerConfig{}, true
	case checkerdef.CheckTypeBrowser:
		return &browserconfig.BrowserConfig{}, true
	case checkerdef.CheckTypeFreeboxLine:
		return &freeboxlineconfig.FreeboxLineConfig{}, true
	case checkerdef.CheckTypeDNSBL:
		return &dnsblconfig.DNSBLConfig{}, true
	case checkerdef.CheckTypeSIP:
		return &sipconfig.SIPConfig{}, true
	case checkerdef.CheckTypeKubernetes:
		return &kubernetesconfig.KubernetesConfig{}, true
	case checkerdef.CheckTypeNTP:
		return &ntpconfig.NTPConfig{}, true
	case checkerdef.CheckTypePrometheus:
		return &prometheusconfig.PrometheusConfig{}, true
	case checkerdef.CheckTypeRDP:
		return &rdpconfig.RDPConfig{}, true
	case checkerdef.CheckTypeSleep:
		return &sleepconfig.SleepConfig{}, true
	default:
		return nil, false
	}
}

// IsKnownType reports whether this build implements a check type. It is the
// existence test the offline validator needs; the heavy registry's GetChecker
// answers the same question for the server, and a test asserts they agree.
func IsKnownType(checkType checkerdef.CheckType) bool {
	_, ok := ParseConfig(checkType)

	return ok
}

// ValidateSpec runs a check type's own offline validation over spec: it parses
// the config, applies every rule and fills in the name/slug defaults, exactly
// as the checker's Validate does — because it IS what the checker's Validate
// calls.
//
// It returns ErrUnknownType for a type this build does not implement, so a
// caller that has not already gone through IsKnownType cannot mistake "no
// validator" for "valid".
//
//nolint:cyclop,funlen // Registry pattern requires a growing switch
func ValidateSpec(checkType checkerdef.CheckType, spec *checkerdef.CheckSpec) error {
	switch checkType {
	case checkerdef.CheckTypeHTTP:
		return httpconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeICMP:
		return icmpconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeDNS:
		return dnsconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeTCP:
		return tcpconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeHeartbeat:
		return heartbeatconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeEmail:
		return emailconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeDomain:
		return domainconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeSSL:
		return sslconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeSMTP:
		return smtpconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeUDP:
		return udpconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeSSH:
		return sshconfig.ValidateSpec(spec)
	case checkerdef.CheckTypePOP3:
		return pop3config.ValidateSpec(spec)
	case checkerdef.CheckTypeIMAP:
		return imapconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeWebSocket:
		return websocketconfig.ValidateSpec(spec)
	case checkerdef.CheckTypePostgreSQL:
		return postgresconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeFTP:
		return ftpconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeSFTP:
		return sftpconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeJS:
		return jsconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeMySQL:
		return mysqlconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeRedis:
		return redisconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeMongoDB:
		return mongodbconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeMSSQL:
		return mssqlconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeOracle:
		return oracleconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeClickHouse:
		return clickhouseconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeGRPC:
		return grpcconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeKafka:
		return kafkaconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeMQTT:
		return mqttconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeA2S:
		return a2sconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeMinecraft:
		return minecraftconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeRabbitMQ:
		return rabbitmqconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeSNMP:
		return snmpconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeDocker:
		return dockerconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeBrowser:
		return browserconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeFreeboxLine:
		return freeboxlineconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeDNSBL:
		return dnsblconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeSIP:
		return sipconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeKubernetes:
		return kubernetesconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeNTP:
		return ntpconfig.ValidateSpec(spec)
	case checkerdef.CheckTypePrometheus:
		return prometheusconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeRDP:
		return rdpconfig.ValidateSpec(spec)
	case checkerdef.CheckTypeSleep:
		return sleepconfig.ValidateSpec(spec)
	default:
		return ErrUnknownType
	}
}

// InferCheckType returns the check type a URL implies, or the empty CheckType
// when none can be inferred. The inference lives in urlparse, which is pure
// string work — the heavy registry exposes the same function and both forward
// to it.
func InferCheckType(urlStr string) checkerdef.CheckType {
	return urlparse.InferCheckType(urlStr)
}

// InferCheckTypeFromConfig examines a config map and infers the check type.
// Returns the empty CheckType when the type cannot be inferred.
func InferCheckTypeFromConfig(config map[string]any) checkerdef.CheckType {
	if url, ok := config["url"].(string); ok && url != "" {
		return InferCheckType(url)
	}

	return ""
}
