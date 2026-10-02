package checksnmp

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestCheckerTypeAndSamples(t *testing.T) {
	t.Parallel()

	c := &SNMPChecker{}
	require.Equal(t, checkerdef.CheckTypeSNMP, c.Type())
	require.NotEmpty(t, c.GetSampleConfigs(&checkerdef.ListSampleOptions{}))
}

func TestCheckerValidate(t *testing.T) {
	t.Parallel()

	c := &SNMPChecker{}
	err := c.Validate(&checkerdef.CheckSpec{
		Name: "x", Slug: "x", Period: time.Minute,
		Config: (&SNMPConfig{}).GetConfig(),
	})
	require.Error(t, err)

	err = c.Validate(&checkerdef.CheckSpec{
		Name: "x", Slug: "x", Period: time.Minute,
		Config: (&SNMPConfig{Host: "h", OID: ".1.3.6.1.2.1.1.1.0"}).GetConfig(),
	})
	require.NoError(t, err)
}

func TestResolveDefaults(t *testing.T) {
	t.Parallel()

	p := resolveDefaults(&SNMPConfig{})
	require.Equal(t, defaultTimeout, p.timeout)
	require.Equal(t, defaultPort, p.port)
	require.Equal(t, defaultVersion, p.version)
	require.Equal(t, defaultOperator, p.operator)

	p = resolveDefaults(&SNMPConfig{Timeout: time.Second, Port: 1, Version: "1", Operator: "contains"})
	require.Equal(t, time.Second, p.timeout)
	require.Equal(t, 1, p.port)
	require.Equal(t, "1", p.version)
	require.Equal(t, "contains", p.operator)
}

func TestBuildClient(t *testing.T) {
	t.Parallel()

	c := &SNMPChecker{}
	tests := []struct {
		name    string
		cfg     SNMPConfig
		version gosnmp.SnmpVersion
		comm    string
		flags   gosnmp.SnmpV3MsgFlags
	}{
		{"v1", SNMPConfig{Host: "h"}, gosnmp.Version1, "public", 0},
		{"v1 community", SNMPConfig{Host: "h", Community: "priv"}, gosnmp.Version1, "priv", 0},
		{"v2c", SNMPConfig{Host: "h"}, gosnmp.Version2c, "public", 0},
		{"v3 noauth", SNMPConfig{Host: "h", Username: "u"}, gosnmp.Version3, "", gosnmp.NoAuthNoPriv},
		{"v3 auth", SNMPConfig{Username: "u", AuthProtocol: "SHA"}, gosnmp.Version3, "", gosnmp.AuthNoPriv},
		{
			"v3 priv",
			SNMPConfig{Username: "u", AuthProtocol: "SHA", PrivProtocol: "AES"},
			gosnmp.Version3, "", gosnmp.AuthPriv,
		},
	}

	versions := map[string]string{"v1": "1", "v1 community": "1", "v2c": "2c"}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ver, ok := versions[tt.name]
			if !ok {
				ver = "3"
			}

			cl := c.buildClient(&tt.cfg, snmpParams{port: 161, version: ver, timeout: time.Second})
			require.Equal(t, tt.version, cl.Version)
			require.Equal(t, tt.comm, cl.Community)

			if tt.version == gosnmp.Version3 {
				require.Equal(t, tt.flags, cl.MsgFlags)
				require.NotNil(t, cl.SecurityParameters)
			}
		})
	}
}

func TestProtocolMapping(t *testing.T) {
	t.Parallel()

	auths := map[string]gosnmp.SnmpV3AuthProtocol{
		"SHA": gosnmp.SHA, "SHA-256": gosnmp.SHA256, "SHA-512": gosnmp.SHA512, "MD5": gosnmp.MD5, "": gosnmp.MD5,
	}
	for in, want := range auths {
		require.Equal(t, want, mapAuthProtocol(in), in)
	}

	privs := map[string]gosnmp.SnmpV3PrivProtocol{
		"AES": gosnmp.AES, "AES-192": gosnmp.AES192, "AES-256": gosnmp.AES256, "DES": gosnmp.DES, "": gosnmp.DES,
	}
	for in, want := range privs {
		require.Equal(t, want, mapPrivProtocol(in), in)
	}

	params := buildUSMParams(&SNMPConfig{
		Username: "u", AuthProtocol: "SHA", AuthPassword: "a", PrivProtocol: "AES", PrivPassword: "p",
	})
	require.Equal(t, "u", params.UserName)
	require.Equal(t, "a", params.AuthenticationPassphrase)
	require.Equal(t, "p", params.PrivacyPassphrase)
}

func TestFormatPDUValueAndTypeName(t *testing.T) {
	t.Parallel()

	require.Equal(t, "hello", formatPDUValue(gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: []byte("hello")}))
	require.Equal(t, "42", formatPDUValue(gosnmp.SnmpPDU{Type: gosnmp.Integer, Value: 42}))
	require.Equal(t, "x", formatPDUValue(gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: "x"}))

	require.Equal(t, "OctetString", pduTypeName(gosnmp.OctetString))
	require.Equal(t, "Counter64", pduTypeName(gosnmp.Counter64))
	require.Equal(t, "Unknown", pduTypeName(gosnmp.Null))
}

func TestCompareValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		actual, expected string
		op               string
		want             bool
	}{
		{"equals", "a", "a", "equals", true},
		{"default op", "a", "b", "", false},
		{"contains", "hello world", "wor", "contains", true},
		{"not_equals", "a", "b", "not_equals", true},
		{"gt", "10", "5", "greater_than", true},
		{"gt false", "1", "5", "greater_than", false},
		{"lt", "1", "5", "less_than", true},
		{"gt not numeric", "a", "5", "greater_than", false},
		{"lt bad expected", "1", "x", "less_than", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, compareValue(tt.actual, tt.expected, tt.op))
		})
	}
}

func TestBuildSuccessResult(t *testing.T) {
	t.Parallel()

	cfg := &SNMPConfig{Host: "h", OID: ".1.2", ExpectedValue: "up"}
	pdu := gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: []byte("up")}

	res := buildSuccessResult(pdu, cfg, snmpParams{operator: "equals"}, time.Now())
	require.Equal(t, checkerdef.StatusUp, res.Status)
	require.Equal(t, true, res.Output["match"])

	cfg.ExpectedValue = "down"
	res = buildSuccessResult(pdu, cfg, snmpParams{operator: "equals"}, time.Now())
	require.Equal(t, checkerdef.StatusDown, res.Status)

	cfg.ExpectedValue = ""
	res = buildSuccessResult(pdu, cfg, snmpParams{}, time.Now())
	require.Equal(t, checkerdef.StatusUp, res.Status)
	require.NotContains(t, res.Output, "match")
}

var errStubBoom = errors.New("boom")

func TestErrorHandlers(t *testing.T) {
	t.Parallel()

	cfg := &SNMPConfig{Host: "h", OID: ".1"}
	live := context.Background()
	dead, cancel := context.WithCancel(context.Background())
	cancel()

	err := errStubBoom

	res := handleConnectError(live, err, cfg, time.Now())
	require.Equal(t, checkerdef.StatusDown, res.Status)
	require.Contains(t, res.Output[checkerdef.OutputKeyError], "connection failed")

	res = handleConnectError(dead, err, cfg, time.Now())
	require.Equal(t, checkerdef.StatusTimeout, res.Status)

	res = handleGetError(live, err, cfg, time.Now())
	require.Equal(t, checkerdef.StatusDown, res.Status)
	require.Contains(t, res.Output[checkerdef.OutputKeyError], "SNMP GET failed")

	res = handleGetError(dead, err, cfg, time.Now())
	require.Equal(t, checkerdef.StatusTimeout, res.Status)
}

func TestExecuteSilentAgent(t *testing.T) {
	t.Parallel()

	pc, err := (&net.ListenConfig{}).ListenPacket(context.Background(), "udp", "127.0.0.1:0")
	require.NoError(t, err)

	defer func() { _ = pc.Close() }()

	udpAddr, ok := pc.LocalAddr().(*net.UDPAddr)
	require.True(t, ok)

	port := udpAddr.Port

	c := &SNMPChecker{}
	res, err := c.Execute(context.Background(), &SNMPConfig{
		Host: "127.0.0.1", Port: port, OID: ".1.3.6.1.2.1.1.1.0", Timeout: 300 * time.Millisecond,
	})
	require.NoError(t, err)
	require.Contains(t, []checkerdef.Status{checkerdef.StatusDown, checkerdef.StatusTimeout}, res.Status)
}

func TestExecuteWrongConfigType(t *testing.T) {
	t.Parallel()

	_, err := (&SNMPChecker{}).Execute(context.Background(), nil)
	require.Error(t, err)
}
