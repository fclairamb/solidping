package tlsedge

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

const unmappedTestDomain = "status.acme.com"

// selfSignedFor mints a throwaway certificate so a test can put real material
// in the certificate cache. The gate under test must refuse the host even when
// a perfectly valid certificate is sitting there ready to be served — without
// this the tests would pass for the wrong reason (certmagic failing to issue).
func selfSignedFor(t *testing.T, host string) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	require.NoError(t, err)

	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// newUnmappedTestEdge builds an edge whose custom-domain gate is a flag the
// test flips, standing in for an operator clearing the domain or the
// re-verification sweep demoting it.
func newUnmappedTestEdge(t *testing.T) (*Edge, *atomic.Bool) {
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

	return edge, servable
}

// helloFor builds the ClientHelloInfo the TLS stack would hand GetCertificate.
//
// Its context cannot be set from outside crypto/tls (ClientHelloInfo.Context()
// reads an unexported field), so a synthetic hello reports a nil context —
// which is exactly why GetCertificate falls back to the edge lifetime context.
func helloFor(host string) *tls.ClientHelloInfo {
	return &tls.ClientHelloInfo{
		ServerName:        host,
		SupportedVersions: []uint16{tls.VersionTLS13},
		CipherSuites:      []uint16{tls.TLS_AES_128_GCM_SHA256},
		SupportedCurves:   []tls.CurveID{tls.X25519},
		SignatureSchemes:  []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256},
	}
}

// expireServableCache ages every memoized positive answer out, standing in for
// the passage of servableCacheTTL.
func (e *Edge) expireServableCache() {
	e.mu.Lock()
	for host := range e.servableCache {
		e.servableCache[host] = time.Now().Add(-time.Second)
	}
	e.mu.Unlock()
}

// TestUnmappedHostRefusedWithCertificateStillCached is THE regression guard for
// the takeover gap, and the only test here that fails if the serving gate in
// TLSConfig is removed.
//
// decide() runs only when certmagic needs to OBTAIN a certificate. Once one was
// cached, certmagic handed it out without asking anyone, so an unmapped domain
// kept completing handshakes for up to 90 days — and with no mapping left the
// request fell through to the instance's own-host routing and served the
// dashboard SPA on a hostname the installation no longer claimed.
//
// What this test deliberately avoids: it never calls ForgetDomain.
// An earlier version did, and it passed with the gate removed — because
// ForgetDomain drops the certificate, so the next handshake took the issuance
// path and was refused by decide() instead. It was green for the wrong reason.
// Leaving the certificate in place is what forces the serving gate to be the
// thing under test.
func TestUnmappedHostRefusedWithCertificateStillCached(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	edge, servable := newUnmappedTestEdge(t)
	ctx := t.Context()

	_, err := edge.magic.CacheUnmanagedTLSCertificate(ctx, selfSignedFor(t, unmappedTestDomain), nil)
	r.NoError(err)

	cfg := edge.TLSConfig(t.Context())

	_, err = cfg.GetCertificate(helloFor(unmappedTestDomain))
	r.NoError(err)

	// Deliberately NO ForgetDomain call: only the mapping is revoked.
	servable.Store(false)

	// The positive cache may still hold the old answer; it is bounded by
	// servableCacheTTL, so expire it the way time would.
	edge.expireServableCache()

	_, err = cfg.GetCertificate(helloFor(unmappedTestDomain))
	r.ErrorIs(err, ErrHostNotAllowed,
		"refusal must not depend on the storage cleanup having run")
}

// TestReservedHostSurvivesCustomDomainRevocation pins that the gate keys off
// hostIsLocal and not merely the custom-domain map: the instance's own
// hostname must keep working when no custom domain is servable at all.
func TestReservedHostSurvivesCustomDomainRevocation(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	edge, servable := newUnmappedTestEdge(t)
	ctx := t.Context()

	servable.Store(false)

	_, err := edge.magic.CacheUnmanagedTLSCertificate(ctx, selfSignedFor(t, "solidping.io"), nil)
	r.NoError(err)

	got, err := edge.TLSConfig(t.Context()).GetCertificate(helloFor("solidping.io"))
	r.NoError(err, "the instance's own host must not be affected by custom-domain state")
	r.NotNil(got)
}

// TestHandshakeWithoutSNIIsRefused pins that a connection carrying no server
// name cannot reach own-host routing either. This one also holds without the
// serving gate (certmagic refuses a nameless hello on its own); it is kept as
// documentation of the contract, not as a guard for this change.
func TestHandshakeWithoutSNIIsRefused(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	edge, _ := newUnmappedTestEdge(t)

	_, err := edge.TLSConfig(t.Context()).GetCertificate(helloFor(""))
	r.ErrorIs(err, ErrHostNotAllowed)
}

// TestForgetDomainEvictsCachedCertificate pins the in-memory half of the
// cleanup: a handshake racing the unmap must not still find the certificate
// sitting in certmagic's cache.
func TestForgetDomainEvictsCachedCertificate(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	edge, _ := newUnmappedTestEdge(t)
	ctx := t.Context()

	_, err := edge.magic.CacheUnmanagedTLSCertificate(ctx, selfSignedFor(t, unmappedTestDomain), nil)
	r.NoError(err)
	r.NotEmpty(edge.cache.AllMatchingCertificates(unmappedTestDomain))

	edge.ForgetDomain(ctx, unmappedTestDomain)

	r.Empty(edge.cache.AllMatchingCertificates(unmappedTestDomain),
		"the certificate must be out of the in-memory cache, not just out of storage")
}

// TestServableCacheDoesNotDelayANewlyVerifiedDomain pins the reason negative
// answers are never cached: a domain verified in the dashboard has to start
// serving on the very next request, not after a TTL.
func TestServableCacheDoesNotDelayANewlyVerifiedDomain(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	edge, servable := newUnmappedTestEdge(t)
	ctx := t.Context()

	// Someone probes the host before it is verified — this must not poison the
	// cache with a negative answer.
	servable.Store(false)
	r.False(edge.hostIsLocal(ctx, unmappedTestDomain))

	// The operator clicks Verify.
	servable.Store(true)

	r.True(edge.hostIsLocal(ctx, unmappedTestDomain),
		"a newly verified domain must be servable immediately, with no TTL to wait out")
}

// TestServableCacheAvoidsRepeatedLookups pins that a positive answer is
// memoized, so a busy status page does not hit the database on every handshake.
func TestServableCacheAvoidsRepeatedLookups(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, dbSvc := newTestStorage(t)

	var lookups atomic.Int64

	edge, err := New(&Options{
		ACME: config.ACMEConfig{Enabled: true, Email: "ops@solidping.io"},
		DB:   dbSvc,
		CustomDomainServable: func(context.Context, string) bool {
			lookups.Add(1)

			return true
		},
	})
	r.NoError(err)

	ctx := t.Context()
	for range 5 {
		r.True(edge.hostIsLocal(ctx, unmappedTestDomain))
	}

	r.Equal(int64(1), lookups.Load(), "repeated handshakes should reuse the cached positive answer")

	// And the memo is dropped the moment the domain stops being ours.
	edge.ForgetDomain(ctx, unmappedTestDomain)
	r.True(edge.hostIsLocal(ctx, unmappedTestDomain))
	r.Equal(int64(2), lookups.Load(), "ForgetDomain must invalidate the memo")
}

// TestForgetDomainRemovesStoredMaterial pins the cleanup half: no private key
// for a domain we no longer serve is left sitting in tls_storage.
func TestForgetDomainRemovesStoredMaterial(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	edge, _ := newUnmappedTestEdge(t)
	ctx := t.Context()

	issuerKey := edge.issuer.IssuerKey()
	base := "certificates/" + issuerKey + "/" + unmappedTestDomain + "/" + unmappedTestDomain

	r.NoError(edge.store.Store(ctx, base+".crt", []byte("pem-cert")))
	r.NoError(edge.store.Store(ctx, base+".key", []byte("pem-key")))
	r.NoError(edge.store.Store(ctx, base+".json", []byte("{}")))

	r.True(edge.store.Exists(ctx, base+".key"))

	edge.ForgetDomain(ctx, unmappedTestDomain)

	r.False(edge.store.Exists(ctx, base+".crt"), "certificate must be gone")
	r.False(edge.store.Exists(ctx, base+".key"), "private key must be gone")
	r.False(edge.store.Exists(ctx, base+".json"), "metadata must be gone")
}
