package dbcontract

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// runEnabledChecksByType pins ListEnabledChecksByType: every organization,
// only the asked types, and only live, enabled, non-internal checks.
const (
	typeEmail = "email"
	typeSMTP  = "smtp"
)

func runEnabledChecksByType(t *testing.T, fix *fixture) {
	t.Helper()

	ctx := t.Context()
	must := require.New(t)
	svc := fix.svc

	other := models.NewOrganization("contract-by-type-org", "Other Org")
	must.NoError(svc.CreateOrganization(ctx, other))

	create := func(orgUID, slug, checkType string, mutate func(*models.Check)) *models.Check {
		check := models.NewCheck(orgUID, slug, checkType)
		if mutate != nil {
			mutate(check)
		}

		must.NoError(svc.CreateCheck(ctx, check))

		return check
	}

	emailA := create(fix.org.UID, "bytype-email-a", typeEmail, nil)
	emailB := create(other.UID, "bytype-email-b", typeEmail, nil)
	smtp := create(fix.org.UID, "bytype-smtp", typeSMTP, nil)
	create(fix.org.UID, "bytype-email-disabled", typeEmail, func(c *models.Check) { c.Enabled = false })
	create(fix.org.UID, "bytype-email-internal", typeEmail, func(c *models.Check) { c.Internal = true })

	deleted := create(fix.org.UID, "bytype-email-deleted", typeEmail, nil)
	must.NoError(svc.DeleteCheck(ctx, deleted.UID))

	uids := func(checks []*models.Check) []string {
		out := make([]string, 0, len(checks))
		for _, check := range checks {
			out = append(out, check.UID)
		}

		return out
	}

	emails, err := svc.ListEnabledChecksByType(ctx, []string{typeEmail})
	must.NoError(err)
	must.ElementsMatch([]string{emailA.UID, emailB.UID}, uids(emails))

	both, err := svc.ListEnabledChecksByType(ctx, []string{typeEmail, typeSMTP})
	must.NoError(err)
	must.ElementsMatch([]string{emailA.UID, emailB.UID, smtp.UID}, uids(both))

	none, err := svc.ListEnabledChecksByType(ctx, []string{"dns"})
	must.NoError(err)
	must.Empty(none)
}
