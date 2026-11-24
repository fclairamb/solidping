package dbcontract

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// runOrgEventFeeds covers the org event feed rows (spec 2026-11-24-12): the
// lookup by secret hash, rotation, last-used stamping and revocation, and the
// org scoping of Get.
func runOrgEventFeeds(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	hash := models.HashOrgEventFeedSecret("spf_contract")
	feed := models.NewOrgEventFeed(fix.org.UID, "NOC wall", hash, []string{"incident.*", "check.created"})
	feed.CreatedByUserUID = &fix.user.UID
	must.NoError(svc.CreateOrgEventFeed(ctx, feed))

	got, err := svc.GetOrgEventFeedBySecretHash(ctx, hash)
	must.NoError(err)
	must.Equal(feed.UID, got.UID)
	must.Equal([]string{"incident.*", "check.created"}, got.EventTypes)
	must.Nil(got.LastUsedAt)

	list, err := svc.ListOrgEventFeeds(ctx, fix.org.UID)
	must.NoError(err)
	must.Len(list, 1)

	otherOrg := models.NewOrganization("contract-org-feeds", "Contract Org Feeds")
	must.NoError(svc.CreateOrganization(ctx, otherOrg))

	_, err = svc.GetOrgEventFeed(ctx, otherOrg.UID, feed.UID)
	must.Error(err, "a feed is only reachable through its own organization")

	now := time.Now().UTC().Truncate(time.Second)
	must.NoError(svc.TouchOrgEventFeed(ctx, feed.UID, now))

	newHash := models.HashOrgEventFeedSecret("spf_contract_rotated")
	must.NoError(svc.RotateOrgEventFeedSecret(ctx, feed.UID, newHash, now))

	_, err = svc.GetOrgEventFeedBySecretHash(ctx, hash)
	must.Error(err, "the old secret stops resolving once rotated")

	got, err = svc.GetOrgEventFeed(ctx, fix.org.UID, feed.UID)
	must.NoError(err)
	must.Equal(newHash, got.SecretHash)
	must.NotNil(got.LastUsedAt)
	must.WithinDuration(now, *got.LastUsedAt, time.Second)
	must.NotNil(got.RotatedAt)

	must.NoError(svc.RevokeOrgEventFeed(ctx, feed.UID))

	_, err = svc.GetOrgEventFeedBySecretHash(ctx, newHash)
	must.Error(err, "a revoked feed stops resolving")

	list, err = svc.ListOrgEventFeeds(ctx, fix.org.UID)
	must.NoError(err)
	must.Empty(list)
}
