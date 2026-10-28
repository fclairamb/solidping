package auth

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// inviteQueryCounter counts the queries bun runs while armed.
type inviteQueryCounter struct {
	armed atomic.Bool
	total atomic.Int64
}

func (c *inviteQueryCounter) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

func (c *inviteQueryCounter) AfterQuery(_ context.Context, _ *bun.QueryEvent) {
	if c.armed.Load() {
		c.total.Add(1)
	}
}

// count returns how many queries fn runs.
func (c *inviteQueryCounter) count(fn func()) int64 {
	c.total.Store(0)
	c.armed.Store(true)
	fn()
	c.armed.Store(false)

	return c.total.Load()
}

// runInviteLookupQueryCount is the regression test of spec 2026-10-28-03:
// the public invite lookup used to probe every organization (about 2 x N
// queries for an unknown token). It must cost the same with 50 more orgs.
func runInviteLookupQueryCount(t *testing.T, svc *Service, dbSvc db.Service) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	counter := &inviteQueryCounter{}
	dbSvc.DB().AddQueryHook(counter)

	org, inviter := inviteHashFixture(t, dbSvc, "ih-qcount")
	resp := createHashTestInvite(t, svc, org.Slug, inviter.UID, "qcount@acme.com")

	const unknown = "1111111111111111111111111111111111111111111111111111111111111111"

	lookUp := func(token string) (int64, error) {
		var err error

		n := counter.count(func() { _, err = svc.GetInviteInfo(ctx, token) })

		return n, err
	}

	unknownBefore, err := lookUp(unknown)
	r.ErrorIs(err, ErrInvitationNotFound)
	r.GreaterOrEqual(unknownBefore, int64(1), "positive control: the hook must fire")

	validBefore, err := lookUp(resp.Token)
	r.NoError(err)

	for i := range 50 {
		r.NoError(dbSvc.CreateOrganization(ctx,
			models.NewOrganization(fmt.Sprintf("ih-qcount-%02d", i), "Query count org")))
	}

	unknownAfter, err := lookUp(unknown)
	r.ErrorIs(err, ErrInvitationNotFound)
	r.Equal(unknownBefore, unknownAfter, "an unknown token must not cost more with more orgs")
	r.LessOrEqual(unknownAfter, int64(2), "an unknown token is one index lookup")

	validAfter, err := lookUp(resp.Token)
	r.NoError(err)
	r.Equal(validBefore, validAfter, "a valid token must not cost more with more orgs")
	r.LessOrEqual(validAfter, int64(4))
}
