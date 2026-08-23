package tlsedge

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
)

const demotedTestDomain = "status.acme.com"

// storeCertificateFor writes usable PEM certificate material for host into
// tls_storage, the way certmagic would after an issuance.
func storeCertificateFor(t *testing.T, dbSvc db.Service, host string, notAfter time.Time) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	require.NoError(t, err)

	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)

	base := "certificates/acme-v02.api.letsencrypt.org-directory/" + host + "/" + host
	ctx := t.Context()
	require.NoError(t, dbSvc.TLSStorageStore(ctx, base+".crt",
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))) //nolint:exhaustruct // pem block
	require.NoError(t, dbSvc.TLSStorageStore(ctx, base+".key",
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))) //nolint:exhaustruct // pem block
}

// newDemotedTestEdge builds an edge whose custom-domain gate is a flag, and
// returns the database behind it so a test can seed stored certificates.
func newDemotedTestEdge(t *testing.T) (*Edge, db.Service, *atomic.Bool) {
	t.Helper()

	_, dbSvc := newTestStorage(t)

	servable := &atomic.Bool{}
	servable.Store(true)

	edge, err := New(&Options{
		ACME:                 config.ACMEConfig{Enabled: true, Email: "ops@solidping.io"},
		DB:                   dbSvc,
		ReservedHosts:        []string{"solidping.io"},
		CustomDomainServable: func(context.Context, string) bool { return servable.Load() },
	})
	require.NoError(t, err)

	return edge, dbSvc, servable
}

// TestDemotedHostWithStoredCertificateAnswersOverHTTP is THE acceptance test for
// requirement 3 of spec 2026-08-23-03, and for the older criterion it makes
// satisfiable at all: "unverified / removed / expired domain degrades to a
// clear message, not a TLS error page" (spec 2026-07-26-01).
//
// It deliberately drives a REAL TLS handshake over a REAL listener rather than
// calling GetCertificate directly, because the claim under test is about the
// handshake completing. The assertion is an HTTP status code and body — which
// can only exist if the handshake finished — not the absence of an error from a
// synthetic hello.
func TestDemotedHostWithStoredCertificateAnswersOverHTTP(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	edge, dbSvc, servable := newDemotedTestEdge(t)
	storeCertificateFor(t, dbSvc, demotedTestDomain, time.Now().Add(30*24*time.Hour))

	// The sweep demoted the domain. The certificate stays in tls_storage —
	// which is exactly the production state that produced
	// "curl: (35) tlsv1 alert internal error".
	servable.Store(false)
	edge.expireServableCache()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	r.NoError(err)

	srv := &http.Server{ //nolint:exhaustruct // only the fields under test matter
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(writer, "Status page unavailable")
		}),
		TLSConfig:         edge.TLSConfig(t.Context()),
		ReadHeaderTimeout: readHeaderTimeout,
	}

	go func() { _ = srv.ServeTLS(listener, "", "") }()

	t.Cleanup(func() { _ = srv.Close() })

	client := &http.Client{ //nolint:exhaustruct // only the transport matters
		Transport: &http.Transport{ //nolint:exhaustruct // only TLS config matters
			TLSClientConfig: &tls.Config{ //nolint:gosec // self-signed fixture
				InsecureSkipVerify: true,
				ServerName:         demotedTestDomain,
			},
		},
		Timeout: 10 * time.Second,
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		"https://"+listener.Addr().String()+"/", nil)
	r.NoError(err)
	req.Host = demotedTestDomain

	resp, err := client.Do(req)
	r.NoError(err, "the handshake must COMPLETE for a domain we hold a certificate for")

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	r.NoError(err)

	r.Equal(http.StatusServiceUnavailable, resp.StatusCode)
	r.Contains(string(body), "Status page unavailable",
		"the visitor must get a legible page, not a browser security interstitial")
}

// TestDemotedHostWithoutACertificateIsStillRefused is the negative control that
// keeps the change from being a hole in the takeover protection: with nothing in
// storage, the gate still refuses — which is what protects the Let's Encrypt
// failed-validation rate limit from a hostile SNI scan.
func TestDemotedHostWithoutACertificateIsStillRefused(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	edge, _, servable := newDemotedTestEdge(t)

	servable.Store(false)
	edge.expireServableCache()

	_, err := edge.TLSConfig(t.Context()).GetCertificate(helloFor(demotedTestDomain))
	r.ErrorIs(err, ErrHostNotAllowed)
}

// TestDemotedHostWithAnExpiredCertificateIsRefused pins that expiry is checked.
// Handing out an expired certificate would produce the same browser
// interstitial the refusal does, with a private key exposure on top.
func TestDemotedHostWithAnExpiredCertificateIsRefused(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	edge, dbSvc, servable := newDemotedTestEdge(t)
	storeCertificateFor(t, dbSvc, demotedTestDomain, time.Now().Add(-time.Minute))

	servable.Store(false)
	edge.expireServableCache()

	_, err := edge.TLSConfig(t.Context()).GetCertificate(helloFor(demotedTestDomain))
	r.ErrorIs(err, ErrHostNotAllowed, "an expired stored certificate is not a certificate")
}

// TestHasValidStoredCertificate covers the helper the re-verification sweep
// gates re-promotion on, including the two ways it must answer false.
func TestHasValidStoredCertificate(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, dbSvc := newTestStorage(t)
	ctx := t.Context()
	now := time.Now()

	r.False(HasValidStoredCertificate(ctx, dbSvc, demotedTestDomain, now), "nothing stored")

	storeCertificateFor(t, dbSvc, demotedTestDomain, now.Add(24*time.Hour))
	r.True(HasValidStoredCertificate(ctx, dbSvc, demotedTestDomain, now))
	r.False(HasValidStoredCertificate(ctx, dbSvc, demotedTestDomain, now.Add(48*time.Hour)),
		"past NotAfter the material is useless")
	r.False(HasValidStoredCertificate(ctx, dbSvc, "other.acme.com", now),
		"a certificate for one host must not answer for another")
}
