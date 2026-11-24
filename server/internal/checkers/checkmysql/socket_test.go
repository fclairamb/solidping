package checkmysql_test

import (
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkmysql"
)

func writeMySQLPacket(conn net.Conn, seq byte, payload []byte) error {
	header := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), seq}

	_, err := conn.Write(append(header, payload...))

	return err
}

func readMySQLPacket(conn net.Conn) ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}

	payload := make([]byte, int(binary.LittleEndian.Uint32(append(header[:3:3], 0))))
	_, err := io.ReadFull(conn, payload)

	return payload, err
}

// mysqlHandshake is a protocol-10 greeting with the minimum capabilities
// (PROTOCOL_41, SECURE_CONNECTION, PLUGIN_AUTH).
func mysqlHandshake() []byte {
	packet := make([]byte, 0, 96)
	packet = append(packet, 10)
	packet = append(packet, "5.7.0-fake\x00"...)
	packet = append(packet, 1, 0, 0, 0)            // connection id
	packet = append(packet, "abcdefgh\x00"...)     // auth data 1 + filler
	packet = append(packet, 0x08, 0x82)            // capabilities (lower)
	packet = append(packet, 33, 2, 0)              // charset, status
	packet = append(packet, 0x08, 0x00, 21)        // capabilities (upper), auth data length
	packet = append(packet, make([]byte, 10)...)   // reserved
	packet = append(packet, "ijklmnopqrst\x00"...) // auth data 2
	packet = append(packet, "mysql_native_password\x00"...)

	return packet
}

// mysqlOK is an OK packet (no rows affected, status 2, no warnings).
var mysqlOK = []byte{0, 0, 0, 2, 0, 0, 0} //nolint:gochecknoglobals // constant packet

// serveMySQL answers the handshake, COM_PING and a one-row, one-column COM_QUERY.
func serveMySQL(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	if writeMySQLPacket(conn, 0, mysqlHandshake()) != nil {
		return
	}

	if _, err := readMySQLPacket(conn); err != nil {
		return
	}

	if writeMySQLPacket(conn, 2, mysqlOK) != nil {
		return
	}

	for {
		payload, err := readMySQLPacket(conn)
		if err != nil || len(payload) == 0 {
			return
		}

		switch payload[0] {
		case 0x0e: // COM_PING
			err = writeMySQLPacket(conn, 1, mysqlOK)
		case 0x03: // COM_QUERY
			err = writeMySQLResult(conn)
		default:
			return
		}

		if err != nil {
			return
		}
	}
}

func writeMySQLResult(conn net.Conn) error {
	column := []byte{3, 'd', 'e', 'f', 0, 0, 0, 1, '1', 0, 0x0c, 33, 0, 1, 0, 0, 0, 0xfd, 0, 0, 0, 0, 0}
	eof := []byte{0xfe, 0, 0, 2, 0}

	for seq, payload := range [][]byte{{1}, column, eof, {1, '1'}, eof} {
		if err := writeMySQLPacket(conn, byte(seq+1), payload); err != nil {
			return err
		}
	}

	return nil
}

func mysqlSocketServer(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("", "mys")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	path := filepath.Join(dir, "m.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	require.NoError(t, err)

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			go serveMySQL(conn)
		}
	}()

	return path
}

func runMySQLSocket(t *testing.T, path string, allow []string) *checkerdef.Result {
	t.Helper()

	cfg := &checkmysql.MySQLConfig{}
	require.NoError(t, cfg.FromMap(map[string]any{"socket": path, "username": "monitor", "timeout": "3s"}))

	ctx := checkerdef.WithLocalSocketAllowlist(t.Context(), allow)

	result, err := (&checkmysql.MySQLChecker{}).Execute(ctx, cfg)
	require.NoError(t, err)

	return result
}

func TestMySQLSocket(t *testing.T) {
	t.Parallel()

	path := mysqlSocketServer(t)
	allow := []string{filepath.Dir(path) + "/"}

	t.Run("allowlisted socket is up", func(t *testing.T) {
		t.Parallel()

		result := runMySQLSocket(t, path, allow)
		require.Equal(t, checkerdef.StatusUp, result.Status, result.Output)
		require.Equal(t, path, result.Output["socket"])
		require.Equal(t, "1", result.Output["result"])
	})

	t.Run("missing socket is down with ENOENT", func(t *testing.T) {
		t.Parallel()

		result := runMySQLSocket(t, filepath.Join(filepath.Dir(path), "missing.sock"), allow)
		require.Equal(t, checkerdef.StatusDown, result.Status, result.Output)
		require.Contains(t, result.Output[checkerdef.OutputKeyError], "ENOENT")
	})

	t.Run("path outside the allowlist is refused without dialing", func(t *testing.T) {
		t.Parallel()

		result := runMySQLSocket(t, path, []string{"/run/other/"})
		require.NotEqual(t, checkerdef.StatusUp, result.Status)
		require.Contains(t, result.Output[checkerdef.OutputKeyError], checkerdef.LocalSocketSetting)

		result = runMySQLSocket(t, path, nil)
		require.NotEqual(t, checkerdef.StatusUp, result.Status, "an empty allowlist refuses every path")
	})
}

func TestMySQLSocketConfig(t *testing.T) {
	t.Parallel()

	validate := func(cfg map[string]any) (*checkerdef.CheckSpec, error) {
		spec := &checkerdef.CheckSpec{Config: cfg}

		return spec, (&checkmysql.MySQLChecker{}).Validate(spec)
	}

	spec, err := validate(map[string]any{"socket": "/run/mysqld/mysqld.sock", "username": "monitor"})
	require.NoError(t, err)
	require.Equal(t, "mysql-run-mysqld-mysqld-sock", spec.Slug)

	_, err = validate(map[string]any{"socket": "/run/mysqld/mysqld.sock", "username": "monitor", "host": "acme.com"})
	require.ErrorContains(t, err, "mutually exclusive")

	_, err = validate(map[string]any{"socket": "mysqld.sock", "username": "monitor"})
	require.Error(t, err, "relative path")

	cfg := &checkmysql.MySQLConfig{}
	require.NoError(t, cfg.FromMap(map[string]any{
		"url": "mysql+unix:///run/mysqld/mysqld.sock?user=monitor&db=app",
	}))
	require.Equal(t, "/run/mysqld/mysqld.sock", cfg.Socket)
	require.Equal(t, "monitor", cfg.Username)
	require.Equal(t, "app", cfg.Database)
}
