package statuspages

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCreateStatusPageHonorsFalseFlags is the reported bug at the layer the
// caller sees it: `POST /orgs/:org/status-pages` with
// `{"showAvailability": false}` answered with `showAvailability: true`, and
// stored true, because a `default:true` bun tag made bun emit the literal
// DEFAULT for the zero value (spec 2026-08-30-04).
//
// Both halves matter. Only asserting the response would pass on a handler that
// echoes the request back without writing it, so the row is re-read from the
// database; only asserting the false case would pass on a create path that
// hard-codes false, so the omitted-field control asserts the true default is
// still there. The dialect-level twins live in
// internal/db/{sqlite,postgres}/zero_value_create*_test.go.
func TestCreateStatusPageHonorsFalseFlags(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ctx, svc, org := setupStatusPagesTest(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name:             "Quiet",
		Slug:             "quiet",
		ShowAvailability: boolPtr(false),
		ShowResponseTime: boolPtr(false),
	})
	r.NoError(err)
	r.False(page.ShowAvailability, "the create response reported the value the caller did not send")
	r.False(page.ShowResponseTime)

	stored, err := svc.db.GetStatusPageBySlug(ctx, org.UID, "quiet")
	r.NoError(err)
	r.False(stored.ShowAvailability, "showAvailability=false never reached the database")
	r.False(stored.ShowResponseTime, "showResponseTime=false never reached the database")

	// Positive control: omitting the flags still yields the true default, now
	// sourced from models.NewStatusPage rather than the column DDL.
	loud, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Loud",
		Slug: "loud",
	})
	r.NoError(err)
	r.True(loud.ShowAvailability)
	r.True(loud.ShowResponseTime)
	r.True(loud.Enabled)

	storedLoud, err := svc.db.GetStatusPageBySlug(ctx, org.UID, "loud")
	r.NoError(err)
	r.True(storedLoud.ShowAvailability)
	r.True(storedLoud.ShowResponseTime)
	r.True(storedLoud.Enabled)
}
