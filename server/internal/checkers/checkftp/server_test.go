package checkftp_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkftp"
)

// fakeFTP is a tiny FTP server: one valid account, one listable directory.
type fakeFTP struct {
	user, pass string
	listOK     bool
}

func (f *fakeFTP) start(t *testing.T) int {
	t.Helper()

	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = lis.Close() })

	go func() {
		for {
			conn, acceptErr := lis.Accept()
			if acceptErr != nil {
				return
			}

			go f.serve(conn)
		}
	}()

	tcp, ok := lis.Addr().(*net.TCPAddr)
	require.True(t, ok)

	return tcp.Port
}

func (f *fakeFTP) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	rd := bufio.NewReader(conn)
	reply := func(s string) { _, _ = fmt.Fprintf(conn, "%s\r\n", s) }

	reply("220 acme ftp ready")

	var data net.Listener

	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return
		}

		cmd, arg, _ := strings.Cut(strings.TrimSpace(line), " ")
		switch strings.ToUpper(cmd) {
		case "FEAT":
			reply("211 no features")
		case "USER":
			if arg == f.user {
				reply("331 password please")
			} else {
				reply("530 unknown user")
			}
		case "PASS":
			if arg == f.pass {
				reply("230 welcome")
			} else {
				reply("530 login incorrect")
			}
		case "TYPE":
			reply("200 ok")
		case "PASV", "EPSV":
			data, err = (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
			if err != nil {
				return
			}

			port := data.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert // test listener

			if strings.ToUpper(cmd) == "EPSV" {
				reply(fmt.Sprintf("229 Entering Extended Passive Mode (|||%d|)", port))
			} else {
				reply(fmt.Sprintf("227 Entering Passive Mode (127,0,0,1,%d,%d)", port/256, port%256))
			}
		case "LIST", "NLST":
			if !f.listOK || data == nil {
				if data != nil {
					_ = data.Close()
				}

				reply("550 no such directory")

				continue
			}

			reply("150 here it comes")

			dc, acceptErr := data.Accept()
			if acceptErr == nil {
				_, _ = dc.Write([]byte("-rw-r--r-- 1 u g 4 Jan 01 00:00 a.txt\r\n"))
				_ = dc.Close()
			}

			_ = data.Close()
			reply("226 done")
		case "QUIT":
			reply("221 bye")

			return
		default:
			reply("502 not implemented")
		}
	}
}

func runFTP(t *testing.T, port int, mutate func(*checkftp.FTPConfig)) *checkerdef.Result {
	t.Helper()

	cfg := &checkftp.FTPConfig{
		Host: "127.0.0.1", Port: port, Timeout: 3 * time.Second,
		Username: "alice", Password: "pw",
	}
	if mutate != nil {
		mutate(cfg)
	}

	res, err := (&checkftp.FTPChecker{}).Execute(t.Context(), cfg)
	require.NoError(t, err)

	return res
}

func TestExecuteAgainstFakeServer(t *testing.T) {
	t.Parallel()

	port := (&fakeFTP{user: "alice", pass: "pw", listOK: true}).start(t)
	noList := (&fakeFTP{user: "alice", pass: "pw"}).start(t)

	t.Run("login ok", func(t *testing.T) {
		t.Parallel()
		res := runFTP(t, port, nil)
		require.Equal(t, checkerdef.StatusUp, res.Status, "%v", res.Output)
		require.Contains(t, res.Metrics, "auth_time_ms")
		require.Contains(t, res.Metrics, "total_time_ms")
	})

	t.Run("path listed", func(t *testing.T) {
		t.Parallel()
		res := runFTP(t, port, func(c *checkftp.FTPConfig) { c.Path = "/pub" })
		require.Equal(t, checkerdef.StatusUp, res.Status, "%v", res.Output)
	})

	t.Run("path rejected", func(t *testing.T) {
		t.Parallel()
		res := runFTP(t, noList, func(c *checkftp.FTPConfig) { c.Path = "/missing" })
		require.Equal(t, checkerdef.StatusDown, res.Status)
		require.Contains(t, res.Output[checkerdef.OutputKeyError], "path verification failed")
	})

	t.Run("wrong password", func(t *testing.T) {
		t.Parallel()
		res := runFTP(t, port, func(c *checkftp.FTPConfig) { c.Password = "bad" })
		require.Equal(t, checkerdef.StatusDown, res.Status)
		require.Contains(t, res.Output[checkerdef.OutputKeyError], "login failed")
	})

	t.Run("connection refused", func(t *testing.T) {
		t.Parallel()
		lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)

		closedPort := lis.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert // test listener
		_ = lis.Close()

		res := runFTP(t, closedPort, nil)
		require.Equal(t, checkerdef.StatusDown, res.Status)
		require.Contains(t, res.Output[checkerdef.OutputKeyError], "connection failed")
	})

	t.Run("default anonymous user", func(t *testing.T) {
		t.Parallel()
		anon := (&fakeFTP{user: "anonymous", pass: ""}).start(t)
		res := runFTP(t, anon, func(c *checkftp.FTPConfig) { c.Username = ""; c.Password = "" })
		require.Equal(t, checkerdef.StatusUp, res.Status, "%v", res.Output)
	})

	t.Run("explicit TLS refused by plain server", func(t *testing.T) {
		t.Parallel()
		res := runFTP(t, port, func(c *checkftp.FTPConfig) { c.TLSMode = checkftp.TLSModeExplicit })
		require.Equal(t, checkerdef.StatusDown, res.Status)
	})

	t.Run("implicit TLS refused by plain server", func(t *testing.T) {
		t.Parallel()
		res := runFTP(t, port, func(c *checkftp.FTPConfig) { c.TLSMode = checkftp.TLSModeImplicit })
		require.Equal(t, checkerdef.StatusDown, res.Status)
	})

	t.Run("canceled context reports timeout", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		cfg := &checkftp.FTPConfig{Host: "127.0.0.1", Port: port, Timeout: time.Second}
		res, err := (&checkftp.FTPChecker{}).Execute(ctx, cfg)
		require.NoError(t, err)
		require.Equal(t, checkerdef.StatusTimeout, res.Status)
	})
}
