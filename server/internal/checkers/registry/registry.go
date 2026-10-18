// Package registry provides factory functions for creating checkers and configs.
package registry

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/fclairamb/solidping/server/internal/checkers/checka2s"
	"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser"
	"github.com/fclairamb/solidping/server/internal/checkers/checkclickhouse"
	"github.com/fclairamb/solidping/server/internal/checkers/checkcrawl"
	"github.com/fclairamb/solidping/server/internal/checkers/checkdns"
	"github.com/fclairamb/solidping/server/internal/checkers/checkdnsbl"
	"github.com/fclairamb/solidping/server/internal/checkers/checkdocker"
	"github.com/fclairamb/solidping/server/internal/checkers/checkdomain"
	"github.com/fclairamb/solidping/server/internal/checkers/checkemail"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkfreeboxline"
	"github.com/fclairamb/solidping/server/internal/checkers/checkftp"
	"github.com/fclairamb/solidping/server/internal/checkers/checkgrpc"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhealth"
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
	"github.com/fclairamb/solidping/server/internal/checkers/checkprivatelocation"
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
	"github.com/fclairamb/solidping/server/internal/checkers/checkvendorstatus"
	"github.com/fclairamb/solidping/server/internal/checkers/checkvnc"
	"github.com/fclairamb/solidping/server/internal/checkers/checkwebsocket"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
)

// entry is one registered check type: how to build its checker and config.
type entry struct {
	newChecker func() checkerdef.Checker
	newConfig  func() checkerdef.Config
}

//nolint:gochecknoglobals // process-wide registry, written at init and at boot
var (
	entriesMu sync.RWMutex
	entries   = map[checkerdef.CheckType]entry{}
)

// builtinCheckers lists every check type compiled into the binary.
//
//nolint:gochecknoglobals // read-only table
var builtinCheckers = []struct {
	checkType  checkerdef.CheckType
	newChecker func() checkerdef.Checker
}{
	{checkerdef.CheckTypeHTTP, func() checkerdef.Checker { return &checkhttp.HTTPChecker{} }},
	{checkerdef.CheckTypeICMP, func() checkerdef.Checker { return &checkicmp.ICMPChecker{} }},
	{checkerdef.CheckTypeDNS, func() checkerdef.Checker { return &checkdns.DNSChecker{} }},
	{checkerdef.CheckTypeTCP, func() checkerdef.Checker { return &checktcp.TCPChecker{} }},
	{checkerdef.CheckTypeHeartbeat, func() checkerdef.Checker { return &checkheartbeat.HeartbeatChecker{} }},
	{checkerdef.CheckTypeEmail, func() checkerdef.Checker { return &checkemail.EmailChecker{} }},
	{checkerdef.CheckTypePrivateLocation, func() checkerdef.Checker {
		return &checkprivatelocation.PrivateLocationChecker{}
	}},
	{checkerdef.CheckTypeDomain, func() checkerdef.Checker { return &checkdomain.DomainChecker{} }},
	{checkerdef.CheckTypeSSL, func() checkerdef.Checker { return &checkssl.SSLChecker{} }},
	{checkerdef.CheckTypeSMTP, func() checkerdef.Checker { return &checksmtp.SMTPChecker{} }},
	{checkerdef.CheckTypeUDP, func() checkerdef.Checker { return &checkudp.UDPChecker{} }},
	{checkerdef.CheckTypeSSH, func() checkerdef.Checker { return &checkssh.SSHChecker{} }},
	{checkerdef.CheckTypePOP3, func() checkerdef.Checker { return &checkpop3.POP3Checker{} }},
	{checkerdef.CheckTypeIMAP, func() checkerdef.Checker { return &checkimap.IMAPChecker{} }},
	{checkerdef.CheckTypeWebSocket, func() checkerdef.Checker { return &checkwebsocket.WebSocketChecker{} }},
	{checkerdef.CheckTypePostgreSQL, func() checkerdef.Checker { return &checkpostgres.PostgreSQLChecker{} }},
	{checkerdef.CheckTypeFTP, func() checkerdef.Checker { return &checkftp.FTPChecker{} }},
	{checkerdef.CheckTypeSFTP, func() checkerdef.Checker { return &checksftp.SFTPChecker{} }},
	{checkerdef.CheckTypeJS, func() checkerdef.Checker { return &checkjs.JSChecker{} }},
	{checkerdef.CheckTypeMySQL, func() checkerdef.Checker { return &checkmysql.MySQLChecker{} }},
	{checkerdef.CheckTypeRedis, func() checkerdef.Checker { return &checkredis.RedisChecker{} }},
	{checkerdef.CheckTypeMongoDB, func() checkerdef.Checker { return &checkmongodb.MongoDBChecker{} }},
	{checkerdef.CheckTypeMSSQL, func() checkerdef.Checker { return &checkmssql.MSSQLChecker{} }},
	{checkerdef.CheckTypeOracle, func() checkerdef.Checker { return &checkoracle.OracleChecker{} }},
	{checkerdef.CheckTypeClickHouse, func() checkerdef.Checker { return &checkclickhouse.ClickHouseChecker{} }},
	{checkerdef.CheckTypeGRPC, func() checkerdef.Checker { return &checkgrpc.GRPCChecker{} }},
	{checkerdef.CheckTypeKafka, func() checkerdef.Checker { return &checkkafka.KafkaChecker{} }},
	{checkerdef.CheckTypeMQTT, func() checkerdef.Checker { return &checkmqtt.MQTTChecker{} }},
	{checkerdef.CheckTypeA2S, func() checkerdef.Checker { return &checka2s.A2SChecker{} }},
	{checkerdef.CheckTypeMinecraft, func() checkerdef.Checker { return &checkminecraft.MinecraftChecker{} }},
	{checkerdef.CheckTypeRabbitMQ, func() checkerdef.Checker { return &checkrabbitmq.RabbitMQChecker{} }},
	{checkerdef.CheckTypeSNMP, func() checkerdef.Checker { return &checksnmp.SNMPChecker{} }},
	{checkerdef.CheckTypeDocker, func() checkerdef.Checker { return &checkdocker.DockerChecker{} }},
	{checkerdef.CheckTypeBrowser, func() checkerdef.Checker { return &checkbrowser.BrowserChecker{} }},
	{checkerdef.CheckTypeFreeboxLine, func() checkerdef.Checker { return &checkfreeboxline.FreeboxLineChecker{} }},
	{checkerdef.CheckTypeDNSBL, func() checkerdef.Checker { return &checkdnsbl.DNSBLChecker{} }},
	{checkerdef.CheckTypeSIP, func() checkerdef.Checker { return &checksip.SIPChecker{} }},
	{checkerdef.CheckTypeKubernetes, func() checkerdef.Checker { return &checkkubernetes.KubernetesChecker{} }},
	{checkerdef.CheckTypeNTP, func() checkerdef.Checker { return &checkntp.NTPChecker{} }},
	{checkerdef.CheckTypePrometheus, func() checkerdef.Checker { return &checkprometheus.PrometheusChecker{} }},
	{checkerdef.CheckTypeRDP, func() checkerdef.Checker { return &checkrdp.RDPChecker{} }},
	{checkerdef.CheckTypeVNC, func() checkerdef.Checker { return &checkvnc.VNCChecker{} }},
	{checkerdef.CheckTypeCrawl, func() checkerdef.Checker { return &checkcrawl.CrawlChecker{} }},
	{checkerdef.CheckTypeHealth, func() checkerdef.Checker { return &checkhealth.HealthChecker{} }},
	{checkerdef.CheckTypeVendorStatus, func() checkerdef.Checker { return &checkvendorstatus.VendorStatusChecker{} }},
	{checkerdef.CheckTypeSleep, func() checkerdef.Checker { return &checksleep.SleepChecker{} }},
}

//nolint:gochecknoinits // Registers the built-ins and breaks the import cycle between checkjs and registry
func init() {
	for _, builtin := range builtinCheckers {
		meta := checkerdef.GetCheckTypeMeta(builtin.checkType)
		if meta == nil {
			panic("registry: built-in check type without meta: " + string(builtin.checkType))
		}

		checkType := builtin.checkType
		Register(meta, builtin.newChecker, func() checkerdef.Config {
			cfg, _ := configregistry.ParseConfig(checkType)

			return cfg
		})
	}

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

// ErrDuplicateType is returned when a check type is registered twice.
var ErrDuplicateType = errors.New("check type already registered")

// Register adds a check type: its meta, a checker factory and a config
// factory. A built-in registers through here at init; registering a type
// twice is a programming error and panics.
func Register(
	meta *checkerdef.CheckTypeMeta,
	newChecker func() checkerdef.Checker,
	newConfig func() checkerdef.Config,
) {
	if err := register(meta.Type, newChecker, newConfig); err != nil {
		panic("registry: " + err.Error())
	}
}

// RegisterPlugin adds a check type loaded at runtime from a plugin manifest
// (spec 2026-10-17-05). Unlike Register it returns an error, so a bad plugin
// is skipped rather than crashing the boot. It registers the meta (always
// ExcludedFromTally), the config factory and validator in configregistry, and
// the checker factory here.
func RegisterPlugin(
	meta *checkerdef.CheckTypeMeta,
	newChecker func() checkerdef.Checker,
	newConfig func() checkerdef.Config,
	validate func(spec *checkerdef.CheckSpec) error,
) error {
	if err := register(meta.Type, newChecker, newConfig); err != nil {
		return err
	}

	if err := configregistry.RegisterExternal(meta.Type, newConfig, validate); err != nil {
		unregister(meta.Type)

		return fmt.Errorf("registering %s config: %w", meta.Type, err)
	}

	if err := checkerdef.RegisterCheckTypeMeta(meta); err != nil {
		configregistry.UnregisterExternal(meta.Type)
		unregister(meta.Type)

		return fmt.Errorf("registering %s meta: %w", meta.Type, err)
	}

	return nil
}

// UnregisterPlugin removes a type added by RegisterPlugin. Built-ins are never
// unregistered.
func UnregisterPlugin(checkType checkerdef.CheckType) {
	if !checkType.IsPlugin() {
		return
	}

	checkerdef.UnregisterCheckTypeMeta(checkType)
	configregistry.UnregisterExternal(checkType)
	unregister(checkType)
}

// PluginTypes returns the plugin check types this process loaded, sorted. A
// worker advertises them at claim time (spec 2026-10-17-05 §17).
func PluginTypes() []string {
	entriesMu.RLock()
	defer entriesMu.RUnlock()

	out := make([]string, 0)

	for checkType := range entries {
		if checkType.IsPlugin() {
			out = append(out, string(checkType))
		}
	}

	sort.Strings(out)

	return out
}

func register(
	checkType checkerdef.CheckType,
	newChecker func() checkerdef.Checker,
	newConfig func() checkerdef.Config,
) error {
	entriesMu.Lock()
	defer entriesMu.Unlock()

	if _, dup := entries[checkType]; dup {
		return fmt.Errorf("%w: %s", ErrDuplicateType, checkType)
	}

	entries[checkType] = entry{newChecker: newChecker, newConfig: newConfig}

	return nil
}

func unregister(checkType checkerdef.CheckType) {
	entriesMu.Lock()
	defer entriesMu.Unlock()

	delete(entries, checkType)
}

func lookup(checkType checkerdef.CheckType) (entry, bool) {
	entriesMu.RLock()
	defer entriesMu.RUnlock()

	found, ok := entries[checkType]

	return found, ok
}

// GetChecker retrieves a checker by type.
// Returns the checker and true if found, nil and false otherwise.
//
//nolint:ireturn // Registry pattern requires interface return
func GetChecker(checkType checkerdef.CheckType) (checkerdef.Checker, bool) {
	found, ok := lookup(checkType)
	if !ok {
		return nil, false
	}

	return found.newChecker(), true
}

// ParseConfig creates the appropriate config struct for a given check type.
// Returns the config interface and true if the type is known, nil and false otherwise.
//
// Built-in configs come from configregistry, the light twin that knows only
// the configs, so the offline validator in `sp` and the server can never
// disagree about what a check type accepts. TestRegistriesAgree pins that.
//
//nolint:ireturn // Registry pattern requires interface return
func ParseConfig(checkType checkerdef.CheckType) (checkerdef.Config, bool) {
	found, ok := lookup(checkType)
	if !ok {
		return nil, false
	}

	return found.newConfig(), true
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
