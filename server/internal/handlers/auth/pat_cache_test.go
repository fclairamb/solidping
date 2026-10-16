package auth

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portPATCachePG is distinct from every other embedded-Postgres test port.
const portPATCachePG = 15564

// The PAT cache honors the token's expiry and every revocation, on every
// replica (spec 2026-10-16-15). Every case runs on both engines.
type patCacheCase struct {
	name string
	// newSvc returns a fresh Service (a "replica") on the shared database.
	run func(t *testing.T, newSvc func() *Service, dbSvc db.Service)
}

func newPATCacheService(dbSvc db.Service) *Service {
	authCfg := config.AuthConfig{
		JWTSecret:          "test-jwt-secret",
		AccessTokenExpiry:  time.Hour,
		RefreshTokenExpiry: 7 * 24 * time.Hour,
	}

	return NewService(dbSvc, authCfg, &config.Config{Auth: authCfg}, nil, nil)
}

func patCached(svc *Service, token string) (*cachedPATClaims, bool) {
	svc.cacheMux.RLock()
	defer svc.cacheMux.RUnlock()

	entry, ok := svc.patCache[sha256HexOf(token)]

	return entry, ok
}

// reconnectingNotifier is a local notifier whose transport can "reconnect",
// like PgEventNotifier's.
type reconnectingNotifier struct {
	*notifier.LocalEventNotifier

	reconnects chan struct{}
}

func (n *reconnectingNotifier) ReconnectEvents() <-chan struct{} { return n.reconnects }

func patCacheCases() []patCacheCase {
	return []patCacheCase{
		{
			name: "an expired PAT is refused even when cached",
			run: func(t *testing.T, newSvc func() *Service, dbSvc db.Service) {
				t.Helper()
				r := require.New(t)
				ctx := t.Context()
				svc := newSvc()
				user, org := tokenHashFixture(ctx, t, dbSvc)

				expiresAt := time.Now().Add(2 * time.Second)
				pat, err := svc.CreatePAT(ctx, org.Slug, user.UID, CreateTokenRequest{Name: "short", ExpiresAt: &expiresAt})
				r.NoError(err)

				_, err = svc.ValidatePATToken(ctx, pat.Token)
				r.NoError(err)

				entry, ok := patCached(svc, pat.Token)
				r.True(ok, "the PAT is cached")
				r.False(entry.expiresAt.After(expiresAt), "the entry never outlives the token")

				r.Eventually(func() bool {
					_, err = svc.ValidatePATToken(ctx, pat.Token)

					return err != nil
				}, 10*time.Second, 100*time.Millisecond, "the cache must not serve an expired PAT")
				r.ErrorIs(err, ErrTokenExpired)

				// Positive control: a PAT without expiry is served from the
				// cache, scopes included, for the backstop TTL.
				forever, err := svc.CreatePAT(ctx, org.Slug, user.UID,
					CreateTokenRequest{Name: "forever", Scopes: []string{"mcp"}})
				r.NoError(err)

				_, err = svc.ValidatePATToken(ctx, forever.Token)
				r.NoError(err)

				entry, ok = patCached(svc, forever.Token)
				r.True(ok, "a PAT without expiry is cached")
				r.WithinDuration(time.Now().Add(patCacheTTL), entry.expiresAt, time.Minute)

				claims, err := svc.ValidatePATToken(ctx, forever.Token)
				r.NoError(err)
				r.Equal([]string{"mcp"}, claims.Scopes, "a cache hit keeps the PAT's scopes")
				r.NotSame(entry.claims, claims, "each hit gets its own copy of the claims")
			},
		},
		{
			name: "a PAT revoked on one replica is refused on the other",
			run: func(t *testing.T, newSvc func() *Service, dbSvc db.Service) {
				t.Helper()
				r := require.New(t)
				ctx := t.Context()

				bus := notifier.NewLocalEventNotifier()
				t.Cleanup(func() { _ = bus.Close() })

				replicaA, replicaB := newSvc(), newSvc()
				replicaA.SetEventNotifier(ctx, bus)
				replicaB.SetEventNotifier(ctx, bus)

				user, org := tokenHashFixture(ctx, t, dbSvc)
				leaked, err := replicaA.CreatePAT(ctx, org.Slug, user.UID, CreateTokenRequest{Name: "leaked"})
				r.NoError(err)

				other, err := replicaA.CreatePAT(ctx, org.Slug, user.UID, CreateTokenRequest{Name: "other"})
				r.NoError(err)

				for _, svc := range []*Service{replicaA, replicaB} {
					_, err = svc.ValidatePATToken(ctx, leaked.Token)
					r.NoError(err)
				}

				_, err = replicaB.ValidatePATToken(ctx, other.Token)
				r.NoError(err)

				r.NoError(replicaA.RevokeToken(ctx, user.UID, leaked.UID))

				_, cachedOnA := patCached(replicaA, leaked.Token)
				r.False(cachedOnA, "the revoking replica evicts synchronously")

				r.Eventually(func() bool {
					_, cachedOnB := patCached(replicaB, leaked.Token)

					return !cachedOnB
				}, 5*time.Second, 10*time.Millisecond, "the other replica evicts on the broadcast")

				_, err = replicaB.ValidatePATToken(ctx, leaked.Token)
				r.ErrorIs(err, ErrInvalidToken)

				// Positive control: the eviction is targeted.
				_, ok := patCached(replicaB, other.Token)
				r.True(ok, "another PAT cached on the other replica stays cached")
			},
		},
		{
			name: "a revocation racing a lookup is not re-cached",
			run: func(t *testing.T, newSvc func() *Service, dbSvc db.Service) {
				t.Helper()
				r := require.New(t)
				ctx := t.Context()
				svc := newSvc()
				user, org := tokenHashFixture(ctx, t, dbSvc)

				pat, err := svc.CreatePAT(ctx, org.Slug, user.UID, CreateTokenRequest{Name: "raced"})
				r.NoError(err)

				var once sync.Once

				svc.patFillHook = func() {
					once.Do(func() {
						r.NoError(svc.RevokeToken(context.WithoutCancel(ctx), user.UID, pat.UID))
					})
				}

				// The lookup read the row before the revocation: it may answer,
				// but must not cache.
				_, err = svc.ValidatePATToken(ctx, pat.Token)
				r.NoError(err)

				_, ok := patCached(svc, pat.Token)
				r.False(ok, "a lookup that raced a revocation is not cached")

				_, err = svc.ValidatePATToken(ctx, pat.Token)
				r.ErrorIs(err, ErrInvalidToken)
			},
		},
		{
			name: "a notifier reconnect empties the cache",
			run: func(t *testing.T, newSvc func() *Service, dbSvc db.Service) {
				t.Helper()
				r := require.New(t)
				ctx := t.Context()

				bus := &reconnectingNotifier{
					LocalEventNotifier: notifier.NewLocalEventNotifier(),
					reconnects:         make(chan struct{}, 1),
				}
				t.Cleanup(func() { _ = bus.Close() })

				svc := newSvc()
				svc.SetEventNotifier(ctx, bus)

				user, org := tokenHashFixture(ctx, t, dbSvc)
				pat, err := svc.CreatePAT(ctx, org.Slug, user.UID, CreateTokenRequest{Name: "gap"})
				r.NoError(err)

				_, err = svc.ValidatePATToken(ctx, pat.Token)
				r.NoError(err)

				_, ok := patCached(svc, pat.Token)
				r.True(ok, "the PAT is cached before the reconnect")

				bus.reconnects <- struct{}{}

				r.Eventually(func() bool {
					_, cached := patCached(svc, pat.Token)

					return !cached
				}, 5*time.Second, 10*time.Millisecond, "a reconnect clears the cache")
			},
		},
	}
}

func TestPATCache_SQLite(t *testing.T) {
	t.Parallel()

	for _, tc := range patCacheCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, dbSvc, _ := setupAuthTestService(t)
			tc.run(t, func() *Service { return newPATCacheService(dbSvc) }, dbSvc)
		})
	}
}

//nolint:paralleltest,tparallel // one embedded PG instance shared by every sub-test
func TestPATCache_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portPATCachePG,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	for _, tc := range patCacheCases() {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t, func() *Service { return newPATCacheService(dbSvc) }, dbSvc)
		})
	}
}
