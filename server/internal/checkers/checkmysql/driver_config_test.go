package checkmysql

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

// recordingDialer records the address it was asked to dial and refuses it.
type recordingDialer struct{ network, addr string }

var errRecorded = errors.New("recorded")

func (d *recordingDialer) DialContext(_ context.Context, network, addr string) (net.Conn, error) {
	d.network, d.addr = network, addr

	return nil, errRecorded
}

// naiveDSN is the pre-fix string interpolation, kept only to prove the
// payloads below are live against the real driver parser.
func naiveDSN(c *MySQLConfig) string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?timeout=10s", c.Username, c.Password, c.Host, c.Port, c.Database)
}

func TestBuildDriverConfigPositiveControl(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := buildDriverConfig(&MySQLConfig{
		Host: "db.acme.com", Port: 3307, Username: "monitor", Password: "s3cret",
		Database: "app", Timeout: 5 * time.Second,
	}, nil)

	r.Equal("tcp", cfg.Net)
	r.Equal("db.acme.com:3307", cfg.Addr)
	r.Equal("monitor", cfg.User)
	r.Equal("s3cret", cfg.Passwd)
	r.Equal("app", cfg.DBName)
	r.Equal(5*time.Second, cfg.Timeout)
	r.False(cfg.AllowAllFiles)
	r.Empty(cfg.Params)
	r.Nil(cfg.DialFunc, "untunneled, unguarded: the driver's default dialer applies")

	_, err := mysql.NewConnector(cfg)
	r.NoError(err)
}

func TestBuildDriverConfigDefaults(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := buildDriverConfig(&MySQLConfig{Host: "::1", Username: "u"}, nil)

	r.Equal("[::1]:3306", cfg.Addr)
	r.Equal(defaultTimeout, cfg.Timeout)
	r.Empty(cfg.DBName)
}

// Regression (a): a database name carrying driver params stays data.
func TestBuildDriverConfigDatabaseCannotInjectParams(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	crafted := &MySQLConfig{Host: "db.acme.com", Port: 3306, Username: "u", Database: "x?allowAllFiles=true&z="}

	// Positive control: the same value through a DSN string turns the flag on.
	parsed, err := mysql.ParseDSN(naiveDSN(crafted))
	r.NoError(err)
	r.True(parsed.AllowAllFiles)

	cfg := buildDriverConfig(crafted, nil)
	r.False(cfg.AllowAllFiles)
	r.Equal("x?allowAllFiles=true&z=", cfg.DBName)
	r.Empty(cfg.Params)
}

// Regression (b): a password carrying `@tcp(...)` never moves the address.
func TestBuildDriverConfigPasswordCannotShiftAddress(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	crafted := &MySQLConfig{Host: "db.acme.com", Port: 3306, Username: "u", Password: "X@tcp(10.0.0.5:3306)/e"}

	// Positive control: the pre-fix tunnel rewrite (first `@tcp(` only) is
	// consumed by the password, so the parsed DSN keeps the unguarded `tcp`.
	parsed, err := mysql.ParseDSN(strings.Replace(naiveDSN(crafted), "@tcp(", "@solidping-tunnel(", 1))
	r.NoError(err)
	r.Equal("tcp", parsed.Net)

	dialer := &recordingDialer{}
	cfg := buildDriverConfig(crafted, dialer)
	r.Equal("tcp", cfg.Net)
	r.Equal("db.acme.com:3306", cfg.Addr)
	r.Equal("X@tcp(10.0.0.5:3306)/e", cfg.Passwd)

	// The injected dialer governs the dial whatever network the driver passes.
	r.NotNil(cfg.DialFunc)
	_, err = cfg.DialFunc(t.Context(), "unix", cfg.Addr)
	r.ErrorIs(err, errRecorded)
	r.Equal("tcp", dialer.network)
	r.Equal("db.acme.com:3306", dialer.addr)
}
