package checkvnc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

// VeNCrypt (RFB security type 19): a version exchange, a sub-type list, then
// for the TLS/X509 sub-types a TLS upgrade of the connection followed by the
// inner authentication (Plain, VNC or None).

// VeNCrypt sub-types.
const (
	vencryptPlain     uint32 = 256
	vencryptTLSNone   uint32 = 257
	vencryptTLSVnc    uint32 = 258
	vencryptTLSPlain  uint32 = 259
	vencryptX509None  uint32 = 260
	vencryptX509Vnc   uint32 = 261
	vencryptX509Plain uint32 = 262
	vencryptTLSSASL   uint32 = 263
	vencryptX509SASL  uint32 = 264

	vencryptMajor = 0
	vencryptMinor = 2

	// maxVeNCryptSubtypes caps the list (its count is one byte anyway).
	maxVeNCryptSubtypes = 255
)

// errNoUsableSubtype marks a VeNCrypt negotiation that could not pick any
// sub-type: the run may fall back to the next security type.
var errNoUsableSubtype = errors.New("no usable VeNCrypt sub-type")

// errNoPeerCertificate is a TLS server that presented no certificate.
var errNoPeerCertificate = errors.New("server presented no certificate")

//nolint:gochecknoglobals // immutable lookup table
var vencryptSubtypeNames = map[uint32]string{
	vencryptPlain:     "Plain",
	vencryptTLSNone:   "TLSNone",
	vencryptTLSVnc:    "TLSVnc",
	vencryptTLSPlain:  "TLSPlain",
	vencryptX509None:  "X509None",
	vencryptX509Vnc:   "X509Vnc",
	vencryptX509Plain: "X509Plain",
	vencryptTLSSASL:   "TLSSASL",
	vencryptX509SASL:  "X509SASL",
}

// vencryptSubtypeName returns a human name for a VeNCrypt sub-type.
func vencryptSubtypeName(t uint32) string {
	if name, ok := vencryptSubtypeNames[t]; ok {
		return name
	}

	return "Unknown (" + strconv.FormatUint(uint64(t), 10) + ")"
}

func describeSubtypes(types []uint32) string {
	parts := make([]string, 0, len(types))
	for _, t := range types {
		parts = append(parts, fmt.Sprintf("%s (%d)", vencryptSubtypeName(t), t))
	}

	return strings.Join(parts, ", ")
}

// isAnonymousTLS reports the TLS* sub-types: they run TLS with anonymous
// Diffie-Hellman cipher suites, which crypto/tls does not implement.
func isAnonymousTLS(t uint32) bool {
	return t == vencryptTLSNone || t == vencryptTLSVnc || t == vencryptTLSPlain || t == vencryptTLSSASL
}

// chooseVeNCryptSubtype picks the strongest sub-type the credentials can
// satisfy: X509Plain (needs a username) > X509Vnc > X509None (only with
// requireAuth off: it authenticates nobody). Anonymous TLS, clear-text Plain
// and SASL are never chosen.
func chooseVeNCryptSubtype(offered []uint32, cfg *VNCConfig) (uint32, error) {
	has := func(t uint32) bool {
		for _, o := range offered {
			if o == t {
				return true
			}
		}

		return false
	}

	switch {
	case has(vencryptX509Plain) && cfg.Username != "":
		return vencryptX509Plain, nil
	case has(vencryptX509Vnc):
		return vencryptX509Vnc, nil
	case has(vencryptX509None) && !cfg.RequiresAuth():
		return vencryptX509None, nil
	}

	var reasons []string

	if has(vencryptX509Plain) {
		reasons = append(reasons, "X509Plain needs a username")
	}

	if has(vencryptX509None) {
		reasons = append(reasons, "X509None authenticates nobody (requireAuth is on)")
	}

	for _, t := range offered {
		if isAnonymousTLS(t) {
			reasons = append(reasons, "the TLS* sub-types use anonymous Diffie-Hellman TLS, "+
				"which Go does not support: configure an X.509 certificate on the server (X509* sub-types)")

			break
		}
	}

	msg := "no usable VeNCrypt sub-type; server offers: " + describeSubtypes(offered)
	if len(reasons) > 0 {
		msg += " (" + strings.Join(reasons, "; ") + ")"
	}

	return 0, failure(FailureAuthUnsupported, errNoUsableSubtype, "%s", msg)
}

// vencryptAuthenticate runs the VeNCrypt exchange after security type 19 was
// selected. On success outcome.conn is the TLS connection. The caller reads
// SecurityResult.
func vencryptAuthenticate(ctx context.Context, outcome *authOutcome, cfg *VNCConfig) error {
	subtype, err := vencryptSelectSubtype(outcome.conn, cfg)
	if err != nil {
		return err
	}

	outcome.subtype = subtype

	tlsConn, err := vencryptTLS(ctx, outcome, cfg)
	if err != nil {
		return err
	}

	outcome.conn = tlsConn

	switch subtype {
	case vencryptX509Plain:
		return writePlainCredentials(tlsConn, cfg.Username, cfg.Password)
	case vencryptX509Vnc:
		return vncAuthenticate(tlsConn, cfg.Password)
	default: // X509None: nothing more
		return nil
	}
}

// vencryptSelectSubtype runs the version exchange, reads the sub-type list,
// picks one and waits for the server to accept it.
func vencryptSelectSubtype(conn net.Conn, cfg *VNCConfig) (uint32, error) {
	var version [2]byte
	if _, err := io.ReadFull(conn, version[:]); err != nil {
		return 0, fmt.Errorf("read VeNCrypt version: %w", err)
	}

	if version[0] == vencryptMajor && version[1] < vencryptMinor {
		return 0, failure(FailureAuthUnsupported, errNoUsableSubtype,
			"server speaks VeNCrypt %d.%d; only 0.2 is supported", version[0], version[1])
	}

	if _, err := conn.Write([]byte{vencryptMajor, vencryptMinor}); err != nil {
		return 0, fmt.Errorf("write VeNCrypt version: %w", err)
	}

	var status [2]byte // version ack, then the sub-type count
	if _, err := io.ReadFull(conn, status[:1]); err != nil {
		return 0, fmt.Errorf("read VeNCrypt version ack: %w", err)
	}

	if status[0] != 0 {
		return 0, failure(FailureAuthUnsupported, errNoUsableSubtype, "server refused VeNCrypt version 0.2")
	}

	if _, err := io.ReadFull(conn, status[1:]); err != nil {
		return 0, fmt.Errorf("read VeNCrypt sub-type count: %w", err)
	}

	offered := make([]uint32, min(int(status[1]), maxVeNCryptSubtypes))
	if err := binary.Read(conn, binary.BigEndian, offered); err != nil {
		return 0, fmt.Errorf("read VeNCrypt sub-types: %w", err)
	}

	subtype, err := chooseVeNCryptSubtype(offered, cfg)
	if err != nil {
		return 0, err
	}

	if err = binary.Write(conn, binary.BigEndian, subtype); err != nil {
		return 0, fmt.Errorf("write VeNCrypt sub-type: %w", err)
	}

	var ack [1]byte
	if _, err = io.ReadFull(conn, ack[:]); err != nil {
		return 0, fmt.Errorf("read VeNCrypt sub-type ack: %w", err)
	}

	if ack[0] != 1 {
		return 0, failure(FailureAuthUnsupported, nil,
			"server refused VeNCrypt sub-type %s", vencryptSubtypeName(subtype))
	}

	return subtype, nil
}

// vencryptTLS upgrades the connection. The chain is always read (and the
// leaf kept for the report); it is only verified when tlsVerify is on,
// because VeNCrypt certificates are almost always self-signed.
func vencryptTLS(ctx context.Context, outcome *authOutcome, cfg *VNCConfig) (*tls.Conn, error) {
	tlsConn := tls.Client(outcome.conn, &tls.Config{
		ServerName: cfg.Host,
		MinVersion: tls.VersionTLS12,
		// Verification is done by VerifyConnection below, so the leaf is
		// captured even when it does not verify.
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errNoPeerCertificate
			}

			outcome.cert = state.PeerCertificates[0]

			if !cfg.TLSVerify {
				return nil
			}

			intermediates := x509.NewCertPool()
			for _, cert := range state.PeerCertificates[1:] {
				intermediates.AddCert(cert)
			}

			_, err := outcome.cert.Verify(x509.VerifyOptions{DNSName: cfg.Host, Intermediates: intermediates})

			return err //nolint:wrapcheck // surfaced as the TLS handshake error
		},
	})

	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return nil, failure(FailureTLS, err, "VeNCrypt TLS handshake failed: %v", err)
	}

	return tlsConn, nil
}

// writePlainCredentials sends the VeNCrypt Plain block: uint32 username
// length, uint32 password length, username, password.
func writePlainCredentials(writer io.Writer, username, password string) error {
	buf := make([]byte, 0, 8+len(username)+len(password))
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(username)))
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(password)))
	buf = append(buf, username...)
	buf = append(buf, password...)

	if _, err := writer.Write(buf); err != nil {
		return fmt.Errorf("write VeNCrypt Plain credentials: %w", err)
	}

	return nil
}
