package checkpostgres

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// hostile is made of the characters a conninfo string treats specially. With the
// old escaper, `\'` canceled the inserted escape and the rest became options.
const hostile = `a\' b=c sslmode=disable sslcert=/etc/x passfile=/p \\' 'q`

func TestBuildPQConfigCarriesFieldsByteForByte(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		cfg   PostgreSQLConfig
		field func(pq.Config) string
	}{
		{"host", PostgreSQLConfig{Host: hostile, Username: "u"}, func(c pq.Config) string { return c.Host }},
		{"username", PostgreSQLConfig{Host: "h", Username: hostile}, func(c pq.Config) string { return c.User }},
		{
			"database",
			PostgreSQLConfig{Host: "h", Username: "u", Database: hostile},
			func(c pq.Config) string { return c.Database },
		},
		{
			"password",
			PostgreSQLConfig{Host: "h", Username: "u", Password: hostile},
			func(c pq.Config) string { return c.Password },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			cfg := tc.cfg
			cfg.SSLMode = "verify-full"
			r.NoError(cfg.Validate())

			got := buildPQConfig(&cfg, time.Second)
			r.Equal(hostile, tc.field(got))
			r.Equal(pq.SSLModeVerifyFull, got.SSLMode)
			r.Equal(noFile, got.Passfile)
			r.Equal(noFile, got.SSLCert)
			r.Equal(noFile, got.SSLKey)
			r.Empty(got.SSLRootCert)
			r.Empty(got.Options)
			r.Empty(got.Service)
			r.Empty(got.Runtime)
		})
	}
}

func TestBuildPQConfigOrdinary(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := PostgreSQLConfig{
		Host: "db.acme.com", Port: 6543, Username: "alice", Password: "s3cret",
		Database: "app", SSLMode: "require",
	}
	r.NoError(cfg.Validate())

	got := buildPQConfig(&cfg, 3*time.Second)
	r.Equal("db.acme.com", got.Host)
	r.Equal(uint16(6543), got.Port)
	r.Equal("alice", got.User)
	r.Equal("s3cret", got.Password)
	r.Equal("app", got.Database)
	r.Equal(pq.SSLModeRequire, got.SSLMode)
	r.Equal(3*time.Second, got.ConnectTimeout)

	// Defaults when the check leaves them out.
	got = buildPQConfig(&PostgreSQLConfig{Host: "h", Username: "u"}, time.Second)
	r.Equal(uint16(defaultPort), got.Port)
	r.Equal(defaultDB, got.Database)
	r.Equal(pq.SSLMode(defaultSSLMode), got.SSLMode)
	r.Empty(got.Password)
}

// fakeCleartextServer accepts one connection, asks for a cleartext password and
// returns what the client sent ("" if it sent no password message).
func fakeCleartextServer(t *testing.T) (int, <-chan string) {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	got := make(chan string, 1)

	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			got <- ""

			return
		}
		defer func() { _ = conn.Close() }()

		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		rd := bufio.NewReader(conn)

		// Startup message: int32 length (self-inclusive) then the payload.
		var size uint32
		if binary.Read(rd, binary.BigEndian, &size) != nil || size < 4 {
			got <- ""

			return
		}

		if _, copyErr := io.CopyN(io.Discard, rd, int64(size-4)); copyErr != nil {
			got <- ""

			return
		}

		// AuthenticationCleartextPassword.
		_, _ = conn.Write([]byte{'R', 0, 0, 0, 8, 0, 0, 0, 3})

		tag, tagErr := rd.ReadByte()
		if tagErr != nil || tag != 'p' {
			got <- ""

			return
		}

		if binary.Read(rd, binary.BigEndian, &size) != nil || size < 5 {
			got <- ""

			return
		}

		body := make([]byte, size-4)
		if _, readErr := io.ReadFull(rd, body); readErr != nil {
			got <- ""

			return
		}

		got <- string(body[:len(body)-1]) // drop the NUL terminator

		// ErrorResponse: severity + message, then end of fields.
		msg := "SFATAL\x00C28P01\x00Mnope\x00\x00"
		frame := []byte{'E', 0, 0, 0, 0}
		binary.BigEndian.PutUint32(frame[1:], uint32(len(msg)+4))
		_, _ = conn.Write(append(frame, msg...))
	}()

	return ln.Addr().(*net.TCPAddr).Port, got //nolint:forcetypeassert // tcp listener
}

func runAgainstFake(t *testing.T, password string) string {
	t.Helper()
	r := require.New(t)

	port, got := fakeCleartextServer(t)

	cfg := &PostgreSQLConfig{}
	m := map[string]any{
		"host": "127.0.0.1", "port": float64(port), "username": "alice",
		"ssl_mode": "disable", "timeout": "3s",
	}
	if password != "" {
		m["password"] = password
	}
	r.NoError(cfg.FromMap(m))

	result, err := (&PostgreSQLChecker{}).Execute(t.Context(), cfg)
	r.NoError(err)
	r.NotNil(result)

	select {
	case pw := <-got:
		return pw
	case <-time.After(5 * time.Second):
		r.FailNow("fake server received nothing")

		return ""
	}
}

// The worker's own PG* variables, pgpass files and client certificate never
// reach a check that does not carry them.
func TestCheckIgnoresWorkerPostgresEnvironment(t *testing.T) {
	r := require.New(t)

	home := t.TempDir()
	r.NoError(os.WriteFile(filepath.Join(home, ".pgpass"), []byte("*:*:*:*:home-pgpass\n"), 0o600))

	passfile := filepath.Join(t.TempDir(), "pgpass")
	r.NoError(os.WriteFile(passfile, []byte("*:*:*:*:env-pgpass\n"), 0o600))

	t.Setenv("HOME", home)
	t.Setenv("PGPASSFILE", passfile)
	t.Setenv("PGSSLCERT", filepath.Join(home, "client.crt"))
	t.Setenv("PGSSLKEY", filepath.Join(home, "client.key"))
	t.Setenv("PGSERVICE", "acme")

	built := buildPQConfig(&PostgreSQLConfig{Host: "h", Username: "u"}, time.Second)
	r.Empty(built.Password)
	r.Equal(noFile, built.SSLCert)
	r.Equal(noFile, built.SSLKey)
	r.Empty(built.Service)

	// ~/.pgpass and PGPASSFILE alone (pq ignores files once a password is set).
	r.Empty(runAgainstFake(t, ""))

	t.Setenv("PGPASSWORD", "env-password")
	r.Empty(runAgainstFake(t, ""))

	// Positive control: the check's own password does reach the server.
	r.Equal(hostile, runAgainstFake(t, hostile))
	r.Equal("s3cret", runAgainstFake(t, "s3cret"))
}

func TestNoFileDoesNotExist(t *testing.T) {
	t.Parallel()

	_, err := os.Stat(noFile)
	require.Error(t, err)
}
