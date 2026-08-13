package agentws_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/agents"
	regionshandler "github.com/fclairamb/solidping/server/internal/handlers/regions"
)

// renameOrg performs exactly the two writes auth.UpdateOrgProfile performs for a
// slug change — swap organizations.slug and record the freed slug as an alias.
// Going through the DB rather than the auth service keeps this test about
// region routing; the point being proven is that a rename touches NOTHING else,
// so the fewer moving parts around it the better.
func renameOrg(ctx context.Context, t *testing.T, e *env, newSlug string) {
	t.Helper()
	r := require.New(t)

	previous := e.org.Slug

	r.NoError(e.dbSvc.UpdateOrganization(ctx, e.org.UID, models.OrganizationUpdate{Slug: &newSlug}))
	r.NoError(e.dbSvc.AddOrganizationPreviousSlug(ctx, e.org.UID, previous))

	reloaded, err := e.dbSvc.GetOrganization(ctx, e.org.UID)
	r.NoError(err)
	r.Equal(newSlug, reloaded.Slug)

	e.org = reloaded
}

// regionRowSnapshot is every stored region string in the database, as a
// multiset keyed by "<table>:<value>". Used to prove a rename rewrote NOTHING.
func regionRowSnapshot(ctx context.Context, t *testing.T, e *env) map[string]int {
	t.Helper()
	r := require.New(t)

	out := map[string]int{}

	collect := func(table, query string) {
		rows, err := e.dbSvc.DB().QueryContext(ctx, query) //nolint:rowserrcheck // checked below
		r.NoError(err)

		defer func() { _ = rows.Close() }()

		for rows.Next() {
			var value string
			r.NoError(rows.Scan(&value))
			out[table+":"+value]++
		}

		r.NoError(rows.Err())
	}

	collect("agents", `select region from agents`)
	collect("tokens", `select region from agent_enrollment_tokens`)
	collect("jobs", `select region from check_jobs where region is not null`)
	collect("checks", `select regions from checks where regions is not null`)

	return out
}

// TestAgentEnrolledBeforeRenameServesChecksCreatedAfter is the headline
// regression of spec 2026-08-13-01, reproduced end to end.
//
// Before the fix: the agent froze on `@stonaltech/aws-paris` at enrollment, the
// rename rewrote nothing, and the regions API then advertised
// `@stonal/aws-paris` — so a check created after the rename targeted a string
// no agent was bound to and sat in `validating` forever, silently.
//
// After the fix both sides speak `@aws-paris`, so the rename is a pure metadata
// change: this test asserts BOTH that the claim succeeds and that the rename
// rewrote zero region-bearing rows (which is what makes it safe to keep the
// rename flow ignorant of regions).
func TestAgentEnrolledBeforeRenameServesChecksCreatedAfter(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)
	ctx := t.Context()

	creds, err := credentials.NewService(nil, credentials.ParamStore{})
	r.NoError(err)

	adminSvc := agents.NewService(e.dbSvc, creds, nil)
	regionsSvc := regionshandler.NewService(e.dbSvc)

	// 1. Pre-rename: the org defines a private region and enrolls an agent in it.
	created, err := adminSvc.CreatePrivateRegion(ctx, e.org.Slug, &agents.CreatePrivateRegionRequest{
		Slug: "aws-paris", Name: "AWS Paris",
	})
	r.NoError(err)
	r.Equal("@aws-paris", created.Region)

	minted, err := adminSvc.MintEnrollmentToken(
		ctx, e.org.Slug, &agents.MintEnrollmentTokenRequest{RegionSlug: "aws-paris"}, nil,
	)
	r.NoError(err)

	conn, _, enrolled := e.enroll(minted.Token, "paris-agent")
	r.Equal("@aws-paris", enrolled.Region, "the agent is bound to an org-relative region")

	before := regionRowSnapshot(ctx, t, e)

	// 2. The org is renamed. Nothing about regions is touched by that flow.
	renameOrg(ctx, t, e, "renamed")

	after := regionRowSnapshot(ctx, t, e)
	r.Equal(before, after, "renaming an org must rewrite ZERO stored region strings")

	// 3. Post-rename, the regions API advertises the region a new check will be
	//    pinned to. This is where the incident was born: it used to advertise a
	//    freshly-built `@<new-slug>/aws-paris` nothing was bound to.
	listed, err := regionsSvc.ListOrgRegions(ctx, e.org.Slug)
	r.NoError(err)

	advertised := ""

	for _, def := range listed.Data {
		if def.Private {
			advertised = def.Slug
		}
	}

	r.Equal("@aws-paris", advertised,
		"the regions API must advertise the same string the pre-rename agent is bound to")

	// 4. A check created AFTER the rename, pinned to the advertised region.
	check := e.createCheck("post-rename", []string{advertised})

	// 5. The pre-rename agent picks it up. Before the fix this claim was empty.
	jobsResp := roundTrip(t, conn, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 10,
	})
	r.Equal(agentcrypto.MsgTypeJobs, jobsResp.Type)
	r.Len(jobsResp.Jobs, 1,
		"an agent enrolled BEFORE the rename must serve a check created AFTER it")
	r.Equal(check.UID, jobsResp.Jobs[0].CheckUID)
	r.NotNil(jobsResp.Jobs[0].Region)
	r.Equal("@aws-paris", *jobsResp.Jobs[0].Region)

	// And it can write the result back, which closes the loop.
	ack := roundTrip(t, conn, agentcrypto.ClientFrame{
		Type:   agentcrypto.MsgTypeResult,
		ID:     "r1",
		JobUID: jobsResp.Jobs[0].UID,
		Status: int(models.ResultStatusUp),
	})
	r.Equal(agentcrypto.MsgTypeAck, ack.Type)
}

// TestTwoOrgsShareOnePrivateRegionString is the cross-org security test that
// removing the org slug makes necessary: `@aws-paris` is now unique only WITHIN
// an org, so two tenants can legitimately hold the byte-identical string. Org
// A's agent must never receive, claim, or write results into org B's job — and
// org B's own agent must still get its job (the positive control, without which
// "nothing was claimed" would prove nothing).
func TestTwoOrgsShareOnePrivateRegionString(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)
	ctx := t.Context()

	const shared = "@aws-paris"

	// Org B: same region string, its own check, its own agent.
	orgB := models.NewOrganization("bravo", "Bravo")
	r.NoError(e.dbSvc.CreateOrganization(ctx, orgB))

	checkB := models.NewCheck(orgB.UID, "b-private", "http")
	checkB.Config = models.JSONMap{"url": "https://b.example.com"}
	checkB.Regions = []string{shared}
	r.NoError(e.dbSvc.CreateCheck(ctx, checkB))

	jobsB, err := e.dbSvc.ListCheckJobsByCheckUID(ctx, checkB.UID)
	r.NoError(err)
	r.Len(jobsB, 1, "org B's job exists and is due")

	// Org A (the env's org): same region string, its own check.
	checkA := e.createCheck("a-private", []string{shared})

	// Enroll org A's agent into the SHARED string.
	tokenA, hashA, err := agentcrypto.GenerateEnrollmentToken()
	r.NoError(err)
	r.NoError(e.dbSvc.CreateAgentEnrollmentToken(
		ctx, models.NewAgentEnrollmentToken(e.org.UID, shared, hashA, futureExpiry(), nil)))

	connA, keysA, enrolledA := e.enroll(tokenA, "a-agent")
	r.Equal(shared, enrolledA.Region)

	// RECEIVE + CLAIM: org A sees exactly its own job, never org B's.
	claimA := roundTrip(t, connA, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "ca", MaxJobs: 10,
	})
	r.Equal(agentcrypto.MsgTypeJobs, claimA.Type)
	r.Len(claimA.Jobs, 1, "an identical region string in another org must not widen the claim")
	r.Equal(checkA.UID, claimA.Jobs[0].CheckUID)

	// RESULT: org A cannot write into org B's job even knowing its UID.
	forbidden := roundTrip(t, connA, agentcrypto.ClientFrame{
		Type:   agentcrypto.MsgTypeResult,
		ID:     "ra",
		JobUID: jobsB[0].UID,
		Status: int(models.ResultStatusUp),
	})
	r.Equal(agentcrypto.MsgTypeError, forbidden.Type)
	r.Equal("FORBIDDEN", forbidden.Code)

	// UNSEAL: a blob sealed to org B's region is not openable with org A's
	// agent identity. The seal is addressed to public keys, never to the region
	// string, which is exactly why collapsing the string is safe.
	sealedForB, err := credentials.SealForRecipients(
		[]string{mustAgentRecipient(ctx, t, e, orgB.UID, shared)},
		map[string]any{"password": "b-secret"},
	)
	r.NoError(err)

	_, err = credentials.UnsealWithIdentity(keysA.X25519Identity, sealedForB)
	r.Error(err, "org A's agent must not be able to unseal org B's region-sealed credentials")

	// Positive control: org B's own agent DOES get org B's job under the very
	// same region string.
	tokenB, hashB, err := agentcrypto.GenerateEnrollmentToken()
	r.NoError(err)
	r.NoError(e.dbSvc.CreateAgentEnrollmentToken(
		ctx, models.NewAgentEnrollmentToken(orgB.UID, shared, hashB, futureExpiry(), nil)))

	connB, _, enrolledB := e.enroll(tokenB, "b-agent")
	r.Equal(shared, enrolledB.Region)

	claimB := roundTrip(t, connB, agentcrypto.ClientFrame{
		Type: agentcrypto.MsgTypeClaim, ID: "cb", MaxJobs: 10,
	})
	r.Equal(agentcrypto.MsgTypeJobs, claimB.Type)
	r.Len(claimB.Jobs, 1, "org B's own agent must still serve org B's job")
	r.Equal(checkB.UID, claimB.Jobs[0].CheckUID)
}

// mustAgentRecipient enrolls a throwaway agent in (org, region) purely to get a
// real X25519 recipient to seal to, and returns its public key.
func mustAgentRecipient(ctx context.Context, t *testing.T, e *env, orgUID, region string) string {
	t.Helper()
	r := require.New(t)

	keys, err := agentcrypto.GenerateAgentKeys()
	r.NoError(err)

	token, hash, err := agentcrypto.GenerateEnrollmentToken()
	r.NoError(err)
	r.NoError(e.dbSvc.CreateAgentEnrollmentToken(
		ctx, models.NewAgentEnrollmentToken(orgUID, region, hash, futureExpiry(), nil)))

	_, err = e.dbSvc.EnrollAgent(
		ctx, agentcrypto.HashEnrollmentToken(token), "sealer",
		keys.Ed25519PublicKey, keys.X25519Recipient, agentcrypto.KeyFingerprint(keys.Ed25519PublicKey),
	)
	r.NoError(err)

	return keys.X25519Recipient
}

// futureExpiry is a comfortably-live enrollment-token expiry.
func futureExpiry() time.Time {
	return time.Now().Add(time.Hour)
}
