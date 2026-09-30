package checkvnc

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// selfSignedCert builds a self-signed ECDSA certificate for 127.0.0.1 that
// expires after validFor.
func selfSignedCert(t *testing.T, validFor time.Duration) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "vnc.acme.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(validFor),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:     []string{"vnc.acme.com"},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// vencryptServer scripts a 3.8 server offering VeNCrypt (plus extra types),
// the given sub-types, a TLS upgrade and the inner authentication.
type vencryptServer struct {
	extraTypes []byte
	subtypes   []uint32
	cert       tls.Certificate
	password   string
	// result/reason is the SecurityResult (0 = OK).
	result uint32
	reason string
	// creds receives the username and password a Plain client sent.
	creds chan [2]string
	// after runs on the TLS stream after ServerInit.
	after func(conn net.Conn) error
}

func (s *vencryptServer) run(conn net.Conn) error {
	if err := rfbGreeting(conn, append([]byte{secVeNCrypt}, s.extraTypes...)...); err != nil {
		return err
	}

	chosen, err := readN(conn, 1)
	if err != nil {
		return err
	}

	if chosen[0] != secVeNCrypt {
		return fmt.Errorf("%w: client chose %d, want VeNCrypt", errFakeServer, chosen[0])
	}

	if err = writeAll(conn, []byte{0, 2}); err != nil {
		return err
	}

	version, err := readN(conn, 2)
	if err != nil {
		return err
	}

	if !bytes.Equal(version, []byte{0, 2}) {
		return fmt.Errorf("%w: client VeNCrypt version %v", errFakeServer, version)
	}

	list := []byte{0, byte(len(s.subtypes))}
	for _, st := range s.subtypes {
		list = append(list, u32(st)...)
	}

	if err = writeAll(conn, list); err != nil {
		return err
	}

	raw, err := readN(conn, 4)
	if err != nil {
		// A client that found nothing usable closes here: not an error.
		return nil //nolint:nilerr // expected on the unsupported path
	}

	subtype := binary.BigEndian.Uint32(raw)
	if !slices.Contains(s.subtypes, subtype) {
		return fmt.Errorf("%w: client chose sub-type %d", errFakeServer, subtype)
	}

	if err = writeAll(conn, []byte{1}); err != nil {
		return err
	}

	tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS12})
	if err = tlsConn.HandshakeContext(context.Background()); err != nil {
		return nil //nolint:nilerr // a verifying client rejects the certificate
	}

	return s.innerAuth(tlsConn, subtype)
}

func (s *vencryptServer) innerAuth(conn net.Conn, subtype uint32) error {
	switch subtype {
	case vencryptX509Plain:
		lens, err := readN(conn, 8)
		if err != nil {
			return err
		}

		user, err := readN(conn, int(binary.BigEndian.Uint32(lens[0:4])))
		if err != nil {
			return err
		}

		pass, err := readN(conn, int(binary.BigEndian.Uint32(lens[4:8])))
		if err != nil {
			return err
		}

		if s.creds != nil {
			s.creds <- [2]string{string(user), string(pass)}
		}
	case vencryptX509Vnc:
		if err := writeAll(conn, fixedChallenge()); err != nil {
			return err
		}

		response, err := readN(conn, challengeLen)
		if err != nil {
			return err
		}

		want, _ := vncAuthResponse(s.password, fixedChallenge())
		if s.result == 0 && !bytes.Equal(response, want) {
			return fmt.Errorf("%w: wrong challenge response", errFakeServer)
		}
	}

	if s.result != 0 {
		return writeAll(conn, u32(s.result), reasonBytes(s.reason))
	}

	if err := writeAll(conn, u32(0)); err != nil {
		return err
	}

	if _, err := readN(conn, 1); err != nil { // ClientInit
		return err
	}

	if err := writeAll(conn, serverInitBytes(testW, testH, "vencrypt")); err != nil {
		return err
	}

	if s.after != nil {
		return s.after(conn)
	}

	return nil
}

// TestVeNCryptX509PlainHappyPath: after the TLS upgrade the server receives
// the exact username and password bytes, and the output names the method,
// the sub-type and the certificate.
func TestVeNCryptX509PlainHappyPath(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	creds := make(chan [2]string, 1)
	srv := &vencryptServer{
		subtypes: []uint32{vencryptTLSVnc, vencryptX509Vnc, vencryptX509Plain},
		cert:     selfSignedCert(t, 365*24*time.Hour),
		creds:    creds,
	}
	host, port, errs := fakeServer(t, srv.run)

	result := runCheck(t, &VNCConfig{Host: host, Port: port, Username: "alice", Password: "longer than 8 bytes"})
	r.NoError(serverErr(t, errs))
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	r.Equal([2]string{"alice", "longer than 8 bytes"}, <-creds)
	r.Equal("VeNCrypt", result.Output["securityType"])
	r.Equal("X509Plain", result.Output["vencryptSubtype"])
	r.Equal("CN=vnc.acme.com", result.Output["certSubject"])
	r.Equal(true, result.Output["certSelfSigned"])
	r.Contains(result.Output, "certExpiresAt")
	r.Equal("vencrypt", result.Output["desktopName"])
}

// TestVeNCryptX509VncWithoutUsername: no username skips X509Plain and uses
// the type-2 challenge inside TLS.
func TestVeNCryptX509VncWithoutUsername(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := &vencryptServer{
		subtypes: []uint32{vencryptX509Plain, vencryptX509Vnc},
		cert:     selfSignedCert(t, 365*24*time.Hour),
		password: "pw",
	}
	host, port, errs := fakeServer(t, srv.run)

	result := runCheck(t, &VNCConfig{Host: host, Port: port, Password: "pw"})
	r.NoError(serverErr(t, errs))
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	r.Equal("X509Vnc", result.Output["vencryptSubtype"])
}

// TestVeNCryptTLSVerify: an untrusted self-signed certificate is Down with
// tlsVerify on (and still reported), Up with tlsVerify off (positive control).
func TestVeNCryptTLSVerify(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	cert := selfSignedCert(t, 365*24*time.Hour)

	strict := &vencryptServer{subtypes: []uint32{vencryptX509Plain}, cert: cert}
	host, port, errs := fakeServer(t, strict.run)

	down := runCheck(t, &VNCConfig{Host: host, Port: port, Username: "u", Password: "p", TLSVerify: true})
	r.NoError(serverErr(t, errs))
	r.Equal(checkerdef.StatusDown, down.Status)
	r.Equal(string(FailureTLS), down.Output["failure_code"])
	r.Contains(down.Output["error"], "certificate")
	r.Equal("CN=vnc.acme.com", down.Output["certSubject"], "the certificate is reported either way")

	lenient := &vencryptServer{subtypes: []uint32{vencryptX509Plain}, cert: cert}
	host2, port2, errs2 := fakeServer(t, lenient.run)

	up := runCheck(t, &VNCConfig{Host: host2, Port: port2, Username: "u", Password: "p"})
	r.NoError(serverErr(t, errs2))
	r.Equal(checkerdef.StatusUp, up.Status, up.Output)
}

// TestVeNCryptCertificateExpiry: a certificate inside the warning threshold
// gives Warning, inside the critical one Down, and the same certificate with
// no thresholds is Up (positive control).
func TestVeNCryptCertificateExpiry(t *testing.T) {
	t.Parallel()

	cert := selfSignedCert(t, 5*24*time.Hour+time.Hour)

	for _, tc := range []struct {
		name     string
		warning  int
		critical int
		want     checkerdef.Status
	}{
		{"warning", 30, 0, checkerdef.StatusWarning},
		{"critical", 30, 7, checkerdef.StatusDown},
		{"no thresholds", 0, 0, checkerdef.StatusUp},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			srv := &vencryptServer{subtypes: []uint32{vencryptX509Plain}, cert: cert}
			host, port, errs := fakeServer(t, srv.run)

			result := runCheck(t, &VNCConfig{
				Host: host, Port: port, Username: "u", Password: "p",
				WarningDays: tc.warning, CriticalDays: tc.critical,
			})
			r.NoError(serverErr(t, errs))
			r.Equal(tc.want, result.Status, result.Output)
			r.Equal(5, result.Metrics["days_remaining"])

			if tc.want != checkerdef.StatusUp {
				r.Equal(string(FailureCertExpiry), result.Output["failure_code"])
				r.Contains(result.Output["error"], "expires in 5 days")
			}
		})
	}
}

// TestVeNCryptExpiredCertificate: an expired certificate is Down even
// without thresholds.
func TestVeNCryptExpiredCertificate(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := &vencryptServer{subtypes: []uint32{vencryptX509Plain}, cert: selfSignedCert(t, -time.Minute)}
	host, port, errs := fakeServer(t, srv.run)

	result := runCheck(t, &VNCConfig{Host: host, Port: port, Username: "u", Password: "p"})
	r.NoError(serverErr(t, errs))
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal("server certificate has expired", result.Output["error"])
}

// TestVeNCryptAnonymousTLSOnly: a server offering only TLSNone/TLSVnc is a
// clear AUTH_TYPE_UNSUPPORTED, not a hang (the run finishes well within the
// timeout) nor a panic.
func TestVeNCryptAnonymousTLSOnly(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := &vencryptServer{subtypes: []uint32{vencryptTLSNone, vencryptTLSVnc}}
	host, port, errs := fakeServer(t, srv.run)

	start := time.Now()
	result := runCheck(t, &VNCConfig{Host: host, Port: port, Password: "pw", RequireAuth: boolPtr(false)})
	r.NoError(serverErr(t, errs))
	r.Less(time.Since(start), 2*time.Second)
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal(string(FailureAuthUnsupported), result.Output["failure_code"])
	r.Contains(result.Output["error"], "TLSNone (257), TLSVnc (258)")
	r.Contains(result.Output["error"], "anonymous Diffie-Hellman")
}

// TestVeNCryptWrongPassword: SecurityResult 1 after Plain is AUTH_FAILED
// with the server's reason.
func TestVeNCryptWrongPassword(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := &vencryptServer{
		subtypes: []uint32{vencryptX509Plain}, cert: selfSignedCert(t, 365*24*time.Hour),
		result: 1, reason: "Authentication failed for alice",
	}
	host, port, errs := fakeServer(t, srv.run)

	result := runCheck(t, &VNCConfig{Host: host, Port: port, Username: "alice", Password: "wrong"})
	r.NoError(serverErr(t, errs))
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal(string(FailureAuthFailed), result.Output["failure_code"])
	r.Contains(result.Output["error"], "Authentication failed for alice")
}

// TestChooseVeNCryptSubtype pins the sub-type preference.
func TestChooseVeNCryptSubtype(t *testing.T) {
	t.Parallel()

	withUser := &VNCConfig{Username: "u", Password: "p"}
	noUser := &VNCConfig{Password: "p"}
	open := &VNCConfig{Password: "p", RequireAuth: boolPtr(false)}
	all := []uint32{
		vencryptTLSNone, vencryptTLSVnc, vencryptTLSPlain, vencryptX509None, vencryptX509Vnc, vencryptX509Plain,
	}

	for _, tc := range []struct {
		name    string
		offered []uint32
		cfg     *VNCConfig
		want    uint32
	}{
		{"plain with username", all, withUser, vencryptX509Plain},
		{"vnc without username", all, noUser, vencryptX509Vnc},
		{"x509none only when auth not required", []uint32{vencryptX509None}, open, vencryptX509None},
		{"x509none refused when auth required", []uint32{vencryptX509None}, noUser, 0},
		{"plain only without username", []uint32{vencryptX509Plain}, noUser, 0},
		{"clear-text plain never", []uint32{vencryptPlain}, withUser, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := chooseVeNCryptSubtype(tc.offered, tc.cfg)
			if tc.want == 0 {
				require.ErrorIs(t, err, errNoUsableSubtype)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestVeNCryptControlSession: the scripted-control path (vnc.connect)
// shares the VeNCrypt code and reads frames through the TLS stream.
func TestVeNCryptControlSession(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := &vencryptServer{
		subtypes: []uint32{vencryptX509Plain},
		cert:     selfSignedCert(t, 365*24*time.Hour),
		after: func(conn net.Conn) error {
			if _, err := readN(conn, 20+12+10); err != nil {
				return err
			}

			if err := writeAll(conn, rawUpdate(0, 0, testW, testH, 0, 0, 0xff)); err != nil {
				return err
			}

			_, _ = io.Copy(io.Discard, conn)

			return nil
		},
	}
	host, port, _ := fakeServer(t, srv.run)

	session, err := OpenVNCSession(context.Background(), &VNCConfig{
		Host: host, Port: port, Username: "alice", Password: "pw", Timeout: 3 * time.Second,
	}, nil)
	r.NoError(err)
	t.Cleanup(session.End)

	pixel, err := session.Pixel(1, 1)
	r.NoError(err)
	r.Equal(uint8(0xff), pixel.R)
}
