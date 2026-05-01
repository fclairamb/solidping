// Package checksnmp provides SNMP device monitoring checks.
package checksnmp

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

// SNMPChecker implements the Checker interface for SNMP health checks.
type SNMPChecker struct{}

// Type returns the check type identifier.
func (c *SNMPChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeSNMP
}

// Validate checks if the configuration is valid.
func (c *SNMPChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &SNMPConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	port := cfg.Port
	if port == 0 {
		port = defaultPort
	}

	if spec.Name == "" {
		spec.Name = fmt.Sprintf("SNMP %s:%d %s", cfg.Host, port, cfg.OID)
	}

	if spec.Slug == "" {
		spec.Slug = "snmp-" + strings.ReplaceAll(cfg.Host, ".", "-")
	}

	return nil
}

// Execute performs the SNMP check and returns the result.
func (c *SNMPChecker) Execute(
	ctx context.Context,
	config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*SNMPConfig](config)
	if err != nil {
		return nil, err
	}

	params := resolveDefaults(cfg)

	ctx, cancel := context.WithTimeout(ctx, params.timeout)
	defer cancel()

	start := time.Now()

	client := c.buildClient(cfg, params)
	client.Context = ctx

	if err := client.Connect(); err != nil {
		return handleConnectError(ctx, err, cfg, start), nil
	}

	defer func() { _ = client.Conn.Close() }()

	return c.performGet(ctx, client, cfg, params, start), nil
}

// snmpParams holds resolved execution parameters with defaults applied.
type snmpParams struct {
	timeout  time.Duration
	port     int
	version  string
	operator string
}

func resolveDefaults(cfg *SNMPConfig) snmpParams {
	params := snmpParams{
		timeout:  cfg.Timeout,
		port:     cfg.Port,
		version:  cfg.Version,
		operator: cfg.Operator,
	}

	if params.timeout == 0 {
		params.timeout = defaultTimeout
	}

	if params.port == 0 {
		params.port = defaultPort
	}

	if params.version == "" {
		params.version = defaultVersion
	}

	if params.operator == "" {
		params.operator = defaultOperator
	}

	return params
}

func (c *SNMPChecker) buildClient(cfg *SNMPConfig, params snmpParams) *gosnmp.GoSNMP {
	client := &gosnmp.GoSNMP{
		Target:  cfg.Host,
		Port:    uint16(params.port),
		Timeout: params.timeout,
		Retries: 1,
	}

	switch params.version {
	case "1":
		client.Version = gosnmp.Version1
		client.Community = resolveCommunity(cfg.Community)
	case "3":
		client.Version = gosnmp.Version3
		client.SecurityModel = gosnmp.UserSecurityModel
		client.MsgFlags = buildMsgFlags(cfg)
		client.SecurityParameters = buildUSMParams(cfg)
	default:
		client.Version = gosnmp.Version2c
		client.Community = resolveCommunity(cfg.Community)
	}

	return client
}

func resolveCommunity(community string) string {
	if community == "" {
		return defaultCommunity
	}

	return community
}

func buildMsgFlags(cfg *SNMPConfig) gosnmp.SnmpV3MsgFlags {
	if cfg.PrivProtocol != "" {
		return gosnmp.AuthPriv
	}

	if cfg.AuthProtocol != "" {
		return gosnmp.AuthNoPriv
	}

	return gosnmp.NoAuthNoPriv
}

func buildUSMParams(cfg *SNMPConfig) *gosnmp.UsmSecurityParameters {
	params := &gosnmp.UsmSecurityParameters{
		UserName: cfg.Username,
	}

	if cfg.AuthProtocol != "" {
		params.AuthenticationProtocol = mapAuthProtocol(cfg.AuthProtocol)
		params.AuthenticationPassphrase = cfg.AuthPassword
	}

	if cfg.PrivProtocol != "" {
		params.PrivacyProtocol = mapPrivProtocol(cfg.PrivProtocol)
		params.PrivacyPassphrase = cfg.PrivPassword
	}

	return params
}

func mapAuthProtocol(protocol string) gosnmp.SnmpV3AuthProtocol {
	switch protocol {
	case "SHA":
		return gosnmp.SHA
	case "SHA-256":
		return gosnmp.SHA256
	case "SHA-512":
		return gosnmp.SHA512
	default:
		return gosnmp.MD5
	}
}

func mapPrivProtocol(protocol string) gosnmp.SnmpV3PrivProtocol {
	switch protocol {
	case "AES":
		return gosnmp.AES
	case "AES-192":
		return gosnmp.AES192
	case "AES-256":
		return gosnmp.AES256
	default:
		return gosnmp.DES
	}
}

func (c *SNMPChecker) performGet(
	ctx context.Context,
	client *gosnmp.GoSNMP,
	cfg *SNMPConfig,
	params snmpParams,
	start time.Time,
) *checkerdef.Result {
	result, err := client.Get([]string{cfg.OID})
	if err != nil {
		return handleGetError(ctx, err, cfg, start)
	}

	if len(result.Variables) == 0 {
		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Output: map[string]any{
				checkerdef.OutputKeyHost:  cfg.Host,
				checkerdef.OutputKeyOID:   cfg.OID,
				checkerdef.OutputKeyError: "no variables returned",
			},
		}
	}

	return buildSuccessResult(result.Variables[0], cfg, params, start)
}

func buildSuccessResult(
	variable gosnmp.SnmpPDU,
	cfg *SNMPConfig,
	params snmpParams,
	start time.Time,
) *checkerdef.Result {
	duration := time.Since(start)
	value := formatPDUValue(variable)

	output := map[string]any{
		checkerdef.OutputKeyHost: cfg.Host,
		checkerdef.OutputKeyOID:  cfg.OID,
		"value":                  value,
		"valueType":              pduTypeName(variable.Type),
	}

	status := checkerdef.StatusUp

	if cfg.ExpectedValue != "" {
		match := compareValue(value, cfg.ExpectedValue, params.operator)
		output["expectedValue"] = cfg.ExpectedValue
		output["match"] = match

		if !match {
			status = checkerdef.StatusDown
		}
	}

	return &checkerdef.Result{
		Status:   status,
		Duration: duration,
		Metrics: map[string]any{
			"query_time_ms": float64(duration.Microseconds()) / microsecondsPerMilli,
		},
		Output: output,
	}
}

func formatPDUValue(pdu gosnmp.SnmpPDU) string {
	if pdu.Type == gosnmp.OctetString {
		if bytes, ok := pdu.Value.([]byte); ok {
			return string(bytes)
		}
	}

	return fmt.Sprintf("%v", pdu.Value)
}

func pduTypeName(pduType gosnmp.Asn1BER) string {
	names := map[gosnmp.Asn1BER]string{
		gosnmp.OctetString:      "OctetString",
		gosnmp.Integer:          "Integer",
		gosnmp.Counter32:        "Counter32",
		gosnmp.Counter64:        "Counter64",
		gosnmp.Gauge32:          "Gauge32",
		gosnmp.TimeTicks:        "TimeTicks",
		gosnmp.ObjectIdentifier: "OID",
		gosnmp.IPAddress:        "IPAddress",
	}

	if name, ok := names[pduType]; ok {
		return name
	}

	return "Unknown"
}

func compareValue(actual, expected, operator string) bool {
	switch operator {
	case "contains":
		return strings.Contains(actual, expected)
	case "not_equals":
		return actual != expected
	case "greater_than":
		return compareNumeric(actual, expected, func(a, b float64) bool { return a > b })
	case "less_than":
		return compareNumeric(actual, expected, func(a, b float64) bool { return a < b })
	default:
		return actual == expected
	}
}

func compareNumeric(actual, expected string, cmp func(a, b float64) bool) bool {
	actualNum, errA := strconv.ParseFloat(actual, 64)
	expectedNum, errB := strconv.ParseFloat(expected, 64)

	if errA != nil || errB != nil {
		return false
	}

	return cmp(actualNum, expectedNum)
}

func handleConnectError(
	ctx context.Context,
	err error,
	cfg *SNMPConfig,
	start time.Time,
) *checkerdef.Result {
	if ctx.Err() != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: time.Since(start),
			Output: map[string]any{
				checkerdef.OutputKeyHost:  cfg.Host,
				checkerdef.OutputKeyOID:   cfg.OID,
				checkerdef.OutputKeyError: "connection timeout",
			},
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Output: map[string]any{
			checkerdef.OutputKeyHost:  cfg.Host,
			checkerdef.OutputKeyOID:   cfg.OID,
			checkerdef.OutputKeyError: "connection failed: " + err.Error(),
		},
	}
}

func handleGetError(
	ctx context.Context,
	err error,
	cfg *SNMPConfig,
	start time.Time,
) *checkerdef.Result {
	if ctx.Err() != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: time.Since(start),
			Output: map[string]any{
				checkerdef.OutputKeyHost:  cfg.Host,
				checkerdef.OutputKeyOID:   cfg.OID,
				checkerdef.OutputKeyError: "query timeout",
			},
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Output: map[string]any{
			checkerdef.OutputKeyHost:  cfg.Host,
			checkerdef.OutputKeyOID:   cfg.OID,
			checkerdef.OutputKeyError: "SNMP GET failed: " + err.Error(),
		},
	}
}
