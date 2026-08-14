package tlsedge

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// errTestIssuance is a static stub for a simulated issuance failure.
var errTestIssuance = errors.New("stub challenge failure")

func newTestStorage(t *testing.T) (*Storage, db.Service) {
	t.Helper()

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(t.Context()))
	t.Cleanup(func() { _ = dbSvc.Close() })

	return NewStorage(dbSvc), dbSvc
}

// TestStorageContract exercises the certmagic.Storage contract the library
// relies on, including the fs.ErrNotExist signaling it tests for explicitly.
func TestStorageContract(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	store, _ := newTestStorage(t)
	ctx := t.Context()

	const key = "certificates/testca/status.acme.com/status.acme.com.crt"

	_, err := store.Load(ctx, key)
	r.ErrorIs(err, fs.ErrNotExist, "certmagic branches on fs.ErrNotExist, not on a driver error")

	_, err = store.Stat(ctx, key)
	r.ErrorIs(err, fs.ErrNotExist)

	r.False(store.Exists(ctx, key))

	r.NoError(store.Store(ctx, key, []byte("cert-bytes")))

	loaded, err := store.Load(ctx, key)
	r.NoError(err)
	r.Equal([]byte("cert-bytes"), loaded)
	r.True(store.Exists(ctx, key))

	info, err := store.Stat(ctx, key)
	r.NoError(err)
	r.True(info.IsTerminal)
	r.Equal(int64(len("cert-bytes")), info.Size)
	r.WithinDuration(time.Now(), info.Modified, time.Minute)

	// A pure prefix is a "directory": it exists, and stats as non-terminal.
	dirInfo, err := store.Stat(ctx, "certificates/testca/status.acme.com")
	r.NoError(err)
	r.False(dirInfo.IsTerminal)
	r.True(store.Exists(ctx, "certificates/testca"))

	// Keys are normalized, so a leading slash addresses the same asset.
	r.True(store.Exists(ctx, "/"+key))

	r.NoError(store.Delete(ctx, "certificates/testca/status.acme.com"))
	r.False(store.Exists(ctx, key))
}

func TestStorageListRecursiveAndShallow(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	store, _ := newTestStorage(t)
	ctx := t.Context()

	for _, key := range []string{
		"certificates/testca/a.example.com/a.example.com.crt",
		"certificates/testca/a.example.com/a.example.com.key",
		"certificates/testca/b.example.com/b.example.com.crt",
	} {
		r.NoError(store.Store(ctx, key, []byte(key)))
	}

	recursive, err := store.List(ctx, "certificates/testca", true)
	r.NoError(err)
	r.Len(recursive, 3)

	shallow, err := store.List(ctx, "certificates/testca", false)
	r.NoError(err)
	r.Equal([]string{
		"certificates/testca/a.example.com",
		"certificates/testca/b.example.com",
	}, shallow, "non-recursive listing collapses nested keys to their directory")

	_, err = store.List(ctx, "certificates/nothing", true)
	r.ErrorIs(err, fs.ErrNotExist)
}

func TestStorageLockIsExclusiveAndReleasable(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	store, dbSvc := newTestStorage(t)
	ctx := t.Context()

	const name = "issue_cert_status.acme.com"

	r.NoError(store.Lock(ctx, name))

	// A second Storage over the same database models a peer node: it must not
	// be able to take a live lock.
	peer := NewStorage(dbSvc)
	blocked, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()

	err := peer.Lock(blocked, name)
	r.Error(err, "a live lock must block a peer")
	r.ErrorIs(err, context.DeadlineExceeded)

	r.NoError(store.Unlock(ctx, name))

	// Once released, the peer gets it.
	free, cancelFree := context.WithTimeout(ctx, 5*time.Second)
	defer cancelFree()
	r.NoError(peer.Lock(free, name))
	r.NoError(peer.Unlock(ctx, name))

	// Unlocking a lock this process never held is a no-op, and must not delete
	// a row someone else owns.
	r.NoError(store.Lock(ctx, name))
	r.NoError(peer.Unlock(ctx, name))

	stillBlocked, cancelStill := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancelStill()
	r.Error(peer.Lock(stillBlocked, name), "a foreign Unlock must not free the lock")
	r.NoError(store.Unlock(ctx, name))
}

// TestDecisionFunc is the security gate's table: only the instance's own hosts
// and verified/servable custom domains may reach the CA.
func TestDecisionFunc(t *testing.T) {
	t.Parallel()

	servable := map[string]bool{
		"status.acme.com": true,
	}

	tests := []struct {
		name    string
		host    string
		allowed bool
	}{
		{name: "own base host", host: "solidping.io", allowed: true},
		{name: "docs host", host: "docs.solidping.io", allowed: true},
		{name: "cname target", host: "cname.solidping.io", allowed: true},
		{name: "own host with port", host: "solidping.io:443", allowed: true},
		{name: "own host uppercase", host: "SolidPing.IO", allowed: true},
		{name: "own host trailing dot", host: "solidping.io.", allowed: true},
		{name: "verified custom domain", host: "status.acme.com", allowed: true},
		{name: "unverified custom domain", host: "unverified.acme.com", allowed: false},
		{name: "unknown host", host: "squatter.example.net", allowed: false},
		{name: "empty host", host: "", allowed: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			_, dbSvc := newTestStorage(t)

			edge, err := New(&Options{
				ACME:          config.ACMEConfig{Enabled: true, Email: "ops@solidping.io"},
				DB:            dbSvc,
				ReservedHosts: []string{"cname.solidping.io", "docs.solidping.io", "solidping.io"},
				CustomDomainServable: func(_ context.Context, host string) bool {
					return servable[host]
				},
			})
			r.NoError(err)

			decideErr := edge.Decide(t.Context(), tc.host)
			if tc.allowed {
				r.NoError(decideErr)

				return
			}

			r.ErrorIs(decideErr, ErrHostNotAllowed)
		})
	}
}

// TestNewRejectsMissingWiring proves the edge cannot be built in a state where
// the decision gate is absent (which would default to "allow").
func TestNewRejectsMissingWiring(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, dbSvc := newTestStorage(t)

	_, err := New(nil)
	r.ErrorIs(err, ErrNoDatabase)

	_, err = New(&Options{CustomDomainServable: func(context.Context, string) bool { return true }})
	r.ErrorIs(err, ErrNoDatabase)

	_, err = New(&Options{DB: dbSvc})
	r.ErrorIs(err, ErrNoServableFunc)
}

func TestCertStatusTransitions(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	store, dbSvc := newTestStorage(t)
	ctx := t.Context()

	edge, err := New(&Options{
		ACME:                 config.ACMEConfig{Enabled: true, Email: "ops@solidping.io"},
		DB:                   dbSvc,
		ReservedHosts:        []string{"solidping.io"},
		CustomDomainServable: func(context.Context, string) bool { return true },
	})
	r.NoError(err)

	const domain = "status.acme.com"

	r.Equal(CertStatusNone, edge.CertStatus(domain))

	// An issuance failure surfaces as "error".
	r.NoError(edge.onEvent(ctx, "cert_failed", map[string]any{"identifier": domain, "error": errTestIssuance}))
	r.Equal(CertStatusError, edge.CertStatus(domain))

	// Certificate material in storage wins over a stale error.
	r.NoError(store.Store(ctx, "certificates/testca/"+domain+"/"+domain+".crt", []byte("pem")))
	edge.invalidateStatus(domain)
	r.Equal(CertStatusIssued, edge.CertStatus(domain))

	// A success event clears the recorded error.
	r.NoError(edge.onEvent(ctx, "cert_obtained", map[string]any{"identifier": domain}))
	r.Equal(CertStatusIssued, edge.CertStatus(domain))

	// An unrelated domain is unaffected.
	r.Equal(CertStatusNone, edge.CertStatus("other.example.com"))
}

// TestTLSConfigKeepsALPNChallenge guards the TLS-ALPN-01 solver: dropping
// acmez's ALPN protocol from NextProtos silently breaks that challenge type.
func TestTLSConfigKeepsALPNChallenge(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, dbSvc := newTestStorage(t)

	edge, err := New(&Options{
		ACME:                 config.ACMEConfig{Enabled: true, Email: "ops@solidping.io"},
		DB:                   dbSvc,
		CustomDomainServable: func(context.Context, string) bool { return false },
	})
	r.NoError(err)

	cfg := edge.TLSConfig(t.Context())
	r.NotNil(cfg.GetCertificate)
	r.Contains(cfg.NextProtos, "acme-tls/1")
	r.Contains(cfg.NextProtos, "http/1.1")
}

// Storage must satisfy certmagic's interface (compile-time guard mirrored in
// the test binary so a refactor cannot quietly drop a method).
var _ certmagic.Storage = (*Storage)(nil)
