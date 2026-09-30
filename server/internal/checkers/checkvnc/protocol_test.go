package checkvnc

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func boolPtr(b bool) *bool { return &b }

// TestVersionNegotiation drives the handshake against 3.3, 3.7, 3.8 and an
// odd-minor (macOS-style 3.889) server, asserting the version the client
// answers with on the server side and the negotiated version in the output.
func TestVersionNegotiation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		banner, wantClient, wantVersion string
	}{
		{"RFB 003.003\n", "RFB 003.003\n", "3.3"},
		{"RFB 003.007\n", "RFB 003.007\n", "3.7"},
		{"RFB 003.008\n", "RFB 003.008\n", "3.8"},
		{"RFB 003.889\n", "RFB 003.008\n", "3.8"},
		{"RFB 005.000\n", "RFB 003.008\n", "3.8"},
	}

	for _, tc := range cases {
		t.Run(tc.banner[:11], func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			host, port, errs := fakeServer(t, func(conn net.Conn) error {
				if err := writeAll(conn, []byte(tc.banner)); err != nil {
					return err
				}

				got, err := readN(conn, bannerLen)
				if err != nil {
					return err
				}

				if string(got) != tc.wantClient {
					return fmt.Errorf("%w: client answered %q, want %q", errFakeServer, got, tc.wantClient)
				}

				if tc.wantVersion == "3.3" {
					return writeAll(conn, u32(uint32(secVNCAuth)))
				}

				return writeAll(conn, []byte{2, secVNCAuth, secTight})
			})

			result := runCheck(t, &VNCConfig{Host: host, Port: port})
			r.NoError(serverErr(t, errs))
			r.Equal(checkerdef.StatusUp, result.Status, result.Output)
			r.Equal(tc.wantVersion, result.Output["rfbVersion"])

			types, ok := result.Output["securityTypes"].([]map[string]any)
			r.True(ok)
			r.Equal(2, types[0]["number"])
			r.Equal("VNC Authentication", types[0]["name"])
		})
	}
}

// TestServerRefusedWithReason: an empty security-types list carries the
// server's reason into the down result.
func TestServerRefusedWithReason(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, banner string
		payload      []byte
	}{
		{"3.8", "RFB 003.008\n", append([]byte{0}, reasonBytes("Server is shutting down")...)},
		{"3.3", "RFB 003.003\n", append(u32(0), reasonBytes("Server is shutting down")...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			host, port, errs := fakeServer(t, func(conn net.Conn) error {
				if err := writeAll(conn, []byte(tc.banner)); err != nil {
					return err
				}

				if _, err := readN(conn, bannerLen); err != nil {
					return err
				}

				return writeAll(conn, tc.payload)
			})

			result := runCheck(t, &VNCConfig{Host: host, Port: port})
			r.NoError(serverErr(t, errs))
			r.Equal(checkerdef.StatusDown, result.Status)
			r.Equal(string(FailureServerRefused), result.Output["failure_code"])
			r.Contains(result.Output["error"], "Server is shutting down")
		})
	}
}

// TestServerRefusedLockout: RealVNC's lockout arrives as a type-0 list.
func TestServerRefusedLockout(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	host, port, errs := fakeServer(t, func(conn net.Conn) error {
		if err := writeAll(conn, []byte("RFB 003.008\n")); err != nil {
			return err
		}

		if _, err := readN(conn, bannerLen); err != nil {
			return err
		}

		return writeAll(conn, []byte{0}, reasonBytes("Too many security failures"))
	})

	result := runCheck(t, &VNCConfig{Host: host, Port: port})
	r.NoError(serverErr(t, errs))
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal(string(FailureTooManyAttempts), result.Output["failure_code"])
}

// TestNotRFBServer: an SSH banner is "not an RFB server".
func TestNotRFBServer(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	host, port, errs := fakeServer(t, func(conn net.Conn) error {
		return writeAll(conn, []byte("SSH-2.0-OpenSSH_9.6\r\n"))
	})

	result := runCheck(t, &VNCConfig{Host: host, Port: port})
	r.NoError(serverErr(t, errs))
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal(string(FailureNotRFB), result.Output["failure_code"])
	r.Contains(result.Output["error"], "not an RFB server")
	r.Contains(result.Output["error"], "SSH-2.0")
}

// TestNotRFBShortBanner: a foreign server that writes a few bytes and closes
// is still classified, not reported as a generic read error.
func TestNotRFBShortBanner(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	host, port, errs := fakeServer(t, func(conn net.Conn) error {
		return writeAll(conn, []byte("220 hi\n"))
	})

	result := runCheck(t, &VNCConfig{Host: host, Port: port})
	r.NoError(serverErr(t, errs))
	r.Equal(string(FailureNotRFB), result.Output["failure_code"])
}

// TestRequireAuth: a server offering None is down with requireAuth (the
// default), and up with requireAuth false (positive control).
func TestRequireAuth(t *testing.T) {
	t.Parallel()

	offerNone := func(conn net.Conn) error {
		if err := writeAll(conn, []byte("RFB 003.008\n")); err != nil {
			return err
		}

		if _, err := readN(conn, bannerLen); err != nil {
			return err
		}

		return writeAll(conn, []byte{2, secNone, secVNCAuth})
	}

	t.Run("default requireAuth is down", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		host, port, errs := fakeServer(t, offerNone)

		result := runCheck(t, &VNCConfig{Host: host, Port: port})
		r.NoError(serverErr(t, errs))
		r.Equal(checkerdef.StatusDown, result.Status)
		r.Equal(string(FailureNoAuthOffered), result.Output["failure_code"])
		r.Contains(result.Output["error"], "None (1)")
	})

	t.Run("requireAuth true is down", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		host, port, errs := fakeServer(t, offerNone)

		result := runCheck(t, &VNCConfig{Host: host, Port: port, RequireAuth: boolPtr(true)})
		r.NoError(serverErr(t, errs))
		r.Equal(checkerdef.StatusDown, result.Status)
		r.Equal(string(FailureNoAuthOffered), result.Output["failure_code"])
	})

	t.Run("requireAuth false is up", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		host, port, errs := fakeServer(t, offerNone)

		result := runCheck(t, &VNCConfig{Host: host, Port: port, RequireAuth: boolPtr(false)})
		r.NoError(serverErr(t, errs))
		r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	})
}

// TestConnectionRefused: nothing listening is a down CONNECTION_FAILED.
func TestConnectionRefused(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	r.NoError(err)

	addr, ok := ln.Addr().(*net.TCPAddr)
	r.True(ok)
	r.NoError(ln.Close())

	result := runCheck(t, &VNCConfig{Host: "127.0.0.1", Port: addr.Port})
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal(string(FailureConnection), result.Output["failure_code"])
}

// TestParseBannerRejectsGarbage covers the pure parser directly.
func TestParseBannerRejectsGarbage(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	_, _, err := parseBanner([]byte("RFB 002.000\n"))
	r.ErrorIs(err, errNotRFB)

	_, _, err = parseBanner([]byte("HTTP/1.1 400"))
	r.ErrorIs(err, errNotRFB)

	announced, v, err := parseBanner([]byte("RFB 003.005\n"))
	r.NoError(err)
	r.Equal("3.5", announced)
	r.Equal(version33, v)
}
