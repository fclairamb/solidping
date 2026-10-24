package checksmtp

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

type flowServer struct {
	greeting    string // full first line, default "220 acme ESMTP"
	ehloFail    bool
	startTLS    bool // advertise and perform STARTTLS
	tlsRefused  bool // advertise but answer 454
	authOK      bool
	advertise   string // AUTH mechanisms to advertise ("" = none)
	implicitTLS bool
	cert        tls.Certificate
}

func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "acme.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"acme.test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func (f *flowServer) start(t *testing.T) int {
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

func (f *flowServer) serve(raw net.Conn) {
	defer func() { _ = raw.Close() }()

	conn := raw
	if f.implicitTLS {
		conn = tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{f.cert}, MinVersion: tls.VersionTLS12})
	}

	rd := bufio.NewReader(conn)

	greeting := f.greeting
	if greeting == "" {
		greeting = "220 acme ESMTP ready"
	}

	_, _ = fmt.Fprintf(conn, "%s\r\n", greeting)

	if !strings.HasPrefix(greeting, "220") {
		return
	}

	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return
		}

		upper := strings.ToUpper(strings.TrimSpace(line))

		switch {
		case strings.HasPrefix(upper, "EHLO"):
			if f.ehloFail {
				_, _ = fmt.Fprintf(conn, "502 no\r\n")

				continue
			}

			caps := []string{"acme"}
			if f.startTLS {
				caps = append(caps, "STARTTLS")
			}

			if f.advertise != "" {
				caps = append(caps, "AUTH "+f.advertise)
			}

			for i, c := range caps {
				sep := "-"
				if i == len(caps)-1 {
					sep = " "
				}

				_, _ = fmt.Fprintf(conn, "250%s%s\r\n", sep, c)
			}
		case upper == "STARTTLS":
			if f.tlsRefused {
				_, _ = fmt.Fprintf(conn, "454 TLS not available\r\n")

				continue
			}

			_, _ = fmt.Fprintf(conn, "220 go ahead\r\n")

			tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{f.cert}, MinVersion: tls.VersionTLS12})
			if tlsConn.HandshakeContext(context.Background()) != nil {
				return
			}

			conn = tlsConn
			rd = bufio.NewReader(conn)
		case strings.HasPrefix(upper, "AUTH"):
			if f.authOK {
				_, _ = fmt.Fprintf(conn, "235 ok\r\n")
			} else {
				_, _ = fmt.Fprintf(conn, "535 bad credentials\r\n")
			}
		case upper == "QUIT":
			_, _ = fmt.Fprintf(conn, "221 bye\r\n")

			return
		default:
			_, _ = fmt.Fprintf(conn, "500 ?\r\n")
		}
	}
}

func runFlow(t *testing.T, port int, mutate func(*SMTPConfig)) *checkerdef.Result {
	t.Helper()

	cfg := &SMTPConfig{Host: "127.0.0.1", Port: port, Timeout: 3 * time.Second}
	if mutate != nil {
		mutate(cfg)
	}

	res, err := (&SMTPChecker{}).Execute(context.Background(), cfg)
	require.NoError(t, err)

	return res
}

func errOf(res *checkerdef.Result) string {
	s, _ := res.Output[checkerdef.OutputKeyError].(string)

	return s
}

func TestExecuteFlows(t *testing.T) {
	t.Parallel()

	cert := selfSigned(t)

	plain := (&flowServer{advertise: "PLAIN LOGIN", authOK: true}).start(t)
	badAuth := (&flowServer{advertise: "PLAIN"}).start(t)
	noAuthAdv := (&flowServer{}).start(t)
	tlsOK := (&flowServer{startTLS: true, advertise: "PLAIN", authOK: true, cert: cert}).start(t)
	tlsNo := (&flowServer{startTLS: true, tlsRefused: true, cert: cert}).start(t)
	noStartTLS := (&flowServer{}).start(t)
	greetBad := (&flowServer{greeting: "554 go away"}).start(t)
	ehloBad := (&flowServer{ehloFail: true}).start(t)

	t.Run("handshake and capabilities", func(t *testing.T) {
		t.Parallel()
		res := runFlow(t, plain, nil)
		require.Equal(t, checkerdef.StatusUp, res.Status)
		require.Equal(t, "acme ESMTP ready", res.Output["greeting"])
		require.Equal(t, []string{"PLAIN", "LOGIN"}, res.Output["auth_mechanisms"])
	})

	t.Run("expected greeting matches", func(t *testing.T) {
		t.Parallel()
		res := runFlow(t, plain, func(c *SMTPConfig) { c.ExpectGreeting = "acme" })
		require.Equal(t, checkerdef.StatusUp, res.Status)
	})

	t.Run("expected greeting differs", func(t *testing.T) {
		t.Parallel()
		res := runFlow(t, plain, func(c *SMTPConfig) { c.ExpectGreeting = "postfix" })
		require.Equal(t, checkerdef.StatusDown, res.Status)
		require.Contains(t, errOf(res), "does not contain expected substring")
	})

	t.Run("greeting rejected", func(t *testing.T) {
		t.Parallel()
		res := runFlow(t, greetBad, nil)
		require.Equal(t, checkerdef.StatusDown, res.Status)
		require.Contains(t, errOf(res), "greeting rejected")
	})

	t.Run("EHLO rejected", func(t *testing.T) {
		t.Parallel()
		res := runFlow(t, ehloBad, nil)
		require.Equal(t, checkerdef.StatusDown, res.Status)
		require.Contains(t, errOf(res), "EHLO rejected")
	})

	t.Run("AUTH succeeds", func(t *testing.T) {
		t.Parallel()
		res := runFlow(t, plain, func(c *SMTPConfig) { c.Username = "u"; c.Password = "p"; c.CheckAuth = true })
		require.Equal(t, checkerdef.StatusUp, res.Status, errOf(res))
		require.Equal(t, true, res.Output["authenticated"])
		require.Contains(t, res.Metrics, "auth_time_ms")
	})

	t.Run("AUTH fails", func(t *testing.T) {
		t.Parallel()
		res := runFlow(t, badAuth, func(c *SMTPConfig) { c.Username = "u"; c.Password = "p" })
		require.Equal(t, checkerdef.StatusDown, res.Status)
		require.Contains(t, errOf(res), "AUTH failed")
	})

	t.Run("AUTH required but not advertised", func(t *testing.T) {
		t.Parallel()
		res := runFlow(t, noAuthAdv, func(c *SMTPConfig) { c.CheckAuth = true })
		require.Equal(t, checkerdef.StatusDown, res.Status)
		require.Equal(t, "AUTH not advertised by server", errOf(res))
	})

	t.Run("STARTTLS upgrade then AUTH", func(t *testing.T) {
		t.Parallel()
		res := runFlow(t, tlsOK, func(c *SMTPConfig) {
			c.StartTLS = true
			c.Username = "u"
			c.Password = "p"
		})
		require.Equal(t, checkerdef.StatusUp, res.Status, errOf(res))
		require.Contains(t, res.Output["tls_version"], "TLS 1.")
		require.NotEmpty(t, res.Output["tls_cipher"])
		require.Contains(t, res.Metrics, "starttls_time_ms")
	})

	t.Run("STARTTLS with certificate verification fails on self-signed", func(t *testing.T) {
		t.Parallel()
		res := runFlow(t, tlsOK, func(c *SMTPConfig) { c.StartTLS = true; c.TLSVerify = true })
		require.Equal(t, checkerdef.StatusDown, res.Status)
	})

	t.Run("STARTTLS refused", func(t *testing.T) {
		t.Parallel()
		res := runFlow(t, tlsNo, func(c *SMTPConfig) { c.StartTLS = true })
		require.Equal(t, checkerdef.StatusDown, res.Status)
		require.Contains(t, errOf(res), "STARTTLS rejected")
	})

	t.Run("STARTTLS not advertised", func(t *testing.T) {
		t.Parallel()
		res := runFlow(t, noStartTLS, func(c *SMTPConfig) { c.StartTLS = true })
		require.Equal(t, checkerdef.StatusDown, res.Status)
	})

	t.Run("connection refused", func(t *testing.T) {
		t.Parallel()
		lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)

		port := lis.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert // test listener
		_ = lis.Close()

		res := runFlow(t, port, nil)
		require.Equal(t, checkerdef.StatusDown, res.Status)
		require.Contains(t, errOf(res), "connection failed")
	})

	t.Run("unresolvable host", func(t *testing.T) {
		t.Parallel()
		res := runFlow(t, 25, func(c *SMTPConfig) { c.Host = "nope.invalid" })
		require.NotEqual(t, checkerdef.StatusUp, res.Status)
		require.NotEmpty(t, errOf(res))
	})

	t.Run("silent server times out", func(t *testing.T) {
		t.Parallel()
		lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = lis.Close() })

		go func() {
			for {
				c, acceptErr := lis.Accept()
				if acceptErr != nil {
					return
				}

				t.Cleanup(func() { _ = c.Close() })
			}
		}()

		port := lis.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert // test listener
		res := runFlow(t, port, func(c *SMTPConfig) { c.Timeout = 300 * time.Millisecond })
		require.Equal(t, checkerdef.StatusTimeout, res.Status)
	})
}

func TestExecuteImplicitTLS(t *testing.T) {
	t.Parallel()

	// Implicit TLS is chosen by port 465 only, so the fake cannot be used on an
	// ephemeral port: exercise the dial helper directly instead.
	cert := selfSigned(t)
	port := (&flowServer{implicitTLS: true, cert: cert}).start(t)
	target := fmt.Sprintf("127.0.0.1:%d", port)

	c := &SMTPChecker{}
	dialer := checkerdef.GuardDialerOr(context.Background(), &net.Dialer{})

	conn, _, err := c.dial(context.Background(), dialer, target, "acme.test", true, false)
	require.NoError(t, err)
	_ = conn.Close()

	_, _, err = c.dial(context.Background(), dialer, target, "acme.test", true, true)
	require.ErrorContains(t, err, "TLS handshake failed")

	plainPort := (&flowServer{}).start(t)
	_, _, err = c.dial(context.Background(), dialer, fmt.Sprintf("127.0.0.1:%d", plainPort), "x", true, false)
	require.ErrorContains(t, err, "TLS handshake failed")
}
