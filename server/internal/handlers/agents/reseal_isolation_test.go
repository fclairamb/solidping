package agents_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/agents"
)

// memDEKStore is an in-memory DEKStore so the credentials service can be
// ENABLED in tests (the shared newSetup deliberately runs it disabled, which
// makes ResealRegion a no-op and would hide everything this test is about).
type memDEKStore struct {
	mu   sync.Mutex
	deks map[string][]byte
}

func newMemDEKStore() *memDEKStore {
	return &memDEKStore{deks: map[string][]byte{}}
}

func (s *memDEKStore) LoadDEK(_ context.Context, orgUID string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	wrapped, ok := s.deks[orgUID]

	return wrapped, ok, nil
}

func (s *memDEKStore) SaveDEK(_ context.Context, orgUID string, wrapped []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.deks[orgUID] = wrapped

	return nil
}

// resealOrg is one tenant's worth of fixture: an org, a private region string,
// an enrolled agent (with its keys) and a mixed-mode check pinned to that region.
type resealOrg struct {
	org   *models.Organization
	keys  *agentcrypto.AgentKeys
	check *models.Check
}

// TestResealRegionNeverCrossesOrgs is the reseal verb of the cross-org security
// requirement. Two orgs hold the byte-identical private region `@dc1` — which
// is only possible because the org slug left the string — so `ResealRegion` for
// org A must:
//
//   - re-seal org A's check to org A's CURRENT agents;
//   - leave org B's check byte-identical, even though its region string matches;
//   - produce a blob org B's agent cannot open.
//
// Without the organization_uid predicate on both ListChecks and
// ListActiveAgentsByRegion, any of the three would fail.
func TestResealRegionNeverCrossesOrgs(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	// A REAL master key: with a disabled credentials service ResealRegion
	// returns immediately and this test would pass vacuously.
	creds, err := credentials.NewService(make([]byte, 32), newMemDEKStore())
	r.NoError(err)
	r.True(creds.Enabled(), "the reseal path only runs with an enabled credentials service")

	svc := agents.NewService(dbSvc, creds, nil)

	const sharedRegion = "@dc1"

	setupOrg := func(slug, secret string) resealOrg {
		org := models.NewOrganization(slug, slug)
		r.NoError(dbSvc.CreateOrganization(ctx, org))

		_, regErr := svc.CreatePrivateRegion(ctx, slug, &agents.CreatePrivateRegionRequest{Slug: "dc1"})
		r.NoError(regErr)

		minted, mintErr := svc.MintEnrollmentToken(
			ctx, slug, &agents.MintEnrollmentTokenRequest{RegionSlug: "dc1"}, nil,
		)
		r.NoError(mintErr)
		r.Equal(sharedRegion, minted.Region, "both orgs hold the same org-relative region string")

		keys, keysErr := agentcrypto.GenerateAgentKeys()
		r.NoError(keysErr)

		_, enrollErr := dbSvc.EnrollAgent(
			ctx, agentcrypto.HashEnrollmentToken(minted.Token), slug+"-agent",
			keys.Ed25519PublicKey, keys.X25519Recipient,
			agentcrypto.KeyFingerprint(keys.Ed25519PublicKey),
		)
		r.NoError(enrollErr)

		// A mixed-mode check: config_private is the server-readable envelope
		// ResealRegion decrypts, config_sealed is the blob it rewrites.
		envelope, encErr := creds.EncryptForOrg(ctx, org.UID, map[string]any{"password": secret})
		r.NoError(encErr)

		check := models.NewCheck(org.UID, slug+"-private", "http")
		check.Config = models.JSONMap{"url": "https://" + slug + ".example.com"}
		check.Regions = []string{sharedRegion}
		check.ConfigPrivate = &envelope
		r.NoError(dbSvc.CreateCheck(ctx, check))

		return resealOrg{org: org, keys: keys, check: check}
	}

	alpha := setupOrg("alpha", "alpha-secret")
	bravo := setupOrg("bravo", "bravo-secret")

	// Seal bravo's check up front and record the exact bytes; nothing alpha
	// does may change them.
	bravoSealed, err := credentials.SealForRecipients(
		[]string{bravo.keys.X25519Recipient}, map[string]any{"password": "bravo-secret"},
	)
	r.NoError(err)
	r.NoError(dbSvc.UpdateCheck(ctx, bravo.check.UID, &models.CheckUpdate{ConfigSealed: &bravoSealed}))

	// Alpha's check starts with NO sealed blob, so a successful reseal is
	// visible as "a blob appeared", not as "some bytes changed".
	before, err := dbSvc.GetCheck(ctx, alpha.org.UID, alpha.check.UID)
	r.NoError(err)
	r.True(before.ConfigSealed == nil || *before.ConfigSealed == "")

	svc.ResealRegion(ctx, alpha.org.UID, sharedRegion)

	// Alpha resealed, and only its own agent can open it.
	alphaAfter, err := dbSvc.GetCheck(ctx, alpha.org.UID, alpha.check.UID)
	r.NoError(err)
	r.NotNil(alphaAfter.ConfigSealed)
	r.NotEmpty(*alphaAfter.ConfigSealed)

	opened, err := credentials.UnsealWithIdentity(alpha.keys.X25519Identity, *alphaAfter.ConfigSealed)
	r.NoError(err)
	r.Equal("alpha-secret", opened["password"])

	_, err = credentials.UnsealWithIdentity(bravo.keys.X25519Identity, *alphaAfter.ConfigSealed)
	r.Error(err, "another org's agent must never be a recipient of a reseal it did not belong to")

	// Bravo is untouched, byte for byte.
	bravoAfter, err := dbSvc.GetCheck(ctx, bravo.org.UID, bravo.check.UID)
	r.NoError(err)
	r.NotNil(bravoAfter.ConfigSealed)
	r.Equal(bravoSealed, *bravoAfter.ConfigSealed,
		"ResealRegion on org A must not rewrite org B's check, identical region string or not")

	// And bravo's own agent still opens bravo's blob (positive control: the
	// assertions above would also hold if the fixture were simply broken).
	bravoOpened, err := credentials.UnsealWithIdentity(bravo.keys.X25519Identity, *bravoAfter.ConfigSealed)
	r.NoError(err)
	r.Equal("bravo-secret", bravoOpened["password"])

	// The counterpart: resealing BRAVO's region does rewrite bravo's check —
	// so the "untouched" assertion above is about isolation, not about
	// ResealRegion silently doing nothing.
	svc.ResealRegion(ctx, bravo.org.UID, sharedRegion)

	bravoResealed, err := dbSvc.GetCheck(ctx, bravo.org.UID, bravo.check.UID)
	r.NoError(err)
	r.NotNil(bravoResealed.ConfigSealed)

	reopened, err := credentials.UnsealWithIdentity(bravo.keys.X25519Identity, *bravoResealed.ConfigSealed)
	r.NoError(err)
	r.Equal("bravo-secret", reopened["password"])
}
