package backend_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jmap"
)

// configureEmailInbox seeds the email.inbox system parameter with a fixed
// working address domain — the prerequisite validateSMTPDeliveryConfig /
// DirectBackend both need before send-mode can resolve anything.
func configureEmailInbox(ctx context.Context, t *testing.T, dbSvc db.Service) {
	t.Helper()

	err := dbSvc.SetSystemParameter(ctx, jmap.SystemParameterKey, map[string]any{
		"enabled":       true,
		"sessionUrl":    "https://jmap.example.com/session",
		"addressDomain": "inbox.example.com",
	}, false)
	require.NoError(t, err)
}

// seedEmailCheck creates an email (passive) check carrying a fixed token, so
// tests can predict the resolved recipient address.
func seedEmailCheck(
	ctx context.Context, t *testing.T, dbSvc db.Service, org *models.Organization, slug, token string,
) *models.Check {
	t.Helper()

	check := models.NewCheck(org.UID, slug, string(checkerdef.CheckTypeEmail))
	check.Config = models.JSONMap{"token": token}
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	return check
}

// seedSMTPJob creates a send-mode SMTP check and rewrites its auto-created
// check_jobs row into a due, unleased, fast-lane job — mirroring seedJob in
// direct_test.go but for check type "smtp".
func seedSMTPJob(
	ctx context.Context, t *testing.T, dbSvc db.Service, org *models.Organization, slug string, config models.JSONMap,
) *models.Check {
	t.Helper()

	check := models.NewCheck(org.UID, slug, string(checkerdef.CheckTypeSMTP))
	check.Config = config
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	due := time.Now().Add(-time.Second)

	_, err := dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("config = ?", config).
		Set("scheduled_at = ?", due).
		Set("effective_scheduled_at = ?", due).
		Set("lane = ?", scheduling.LaneFast).
		Where("check_uid = ?", check.UID).
		Exec(ctx)
	require.NoError(t, err)

	return check
}

// sendModeConfig builds a send-mode SMTP check config. mail_from is fixed
// (irrelevant to what these tests exercise); deliveryCheckUID varies per test.
func sendModeConfig(deliveryCheckUID string) models.JSONMap {
	return models.JSONMap{
		"host":               "mail.example.com",
		"send_email":         true,
		"mail_from":          "probe@example.com",
		"delivery_check_uid": deliveryCheckUID,
	}
}

// TestResolveSMTPDelivery_Success pins the happy path: a send-mode SMTP job
// whose delivery_check_uid resolves to a live, same-org email check gets the
// concrete recipient threaded onto its Config under the internal key the
// worker (never SMTPConfig.FromMap) reads.
func TestResolveSMTPDelivery_Success(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	be, _, dbSvc, ctx := newDirectBackend(t)

	org := newOrg(t, ctx, dbSvc, "smtp-resolve-ok")
	configureEmailInbox(ctx, t, dbSvc)

	emailCheck := seedEmailCheck(ctx, t, dbSvc, org, "delivery", "deadbeef000000000000000000000000000000000000aa")
	smtpCheck := seedSMTPJob(ctx, t, dbSvc, org, "probe", sendModeConfig(emailCheck.UID))

	workerUID := registerWorker(ctx, t, dbSvc, "wk-smtp-ok")

	job := claimOne(ctx, t, be, workerUID, smtpCheck.UID)
	r.NotNil(job, "a resolvable send-mode job must be dispatched")
	r.Equal(
		"deadbeef000000000000000000000000000000000000aa@inbox.example.com",
		job.Config[checkerdef.SMTPResolvedRecipientConfigKey],
	)
}

// TestResolveSMTPDelivery_SendModeOff pins the negative control: a plain SMTP
// job (send_email absent/false) is dispatched completely untouched — no
// resolution attempted, no internal key added.
func TestResolveSMTPDelivery_SendModeOff(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	be, _, dbSvc, ctx := newDirectBackend(t)

	org := newOrg(t, ctx, dbSvc, "smtp-resolve-off")
	check := seedSMTPJob(ctx, t, dbSvc, org, "plain", models.JSONMap{"host": "mail.example.com"})
	workerUID := registerWorker(ctx, t, dbSvc, "wk-smtp-off")

	job := claimOne(ctx, t, be, workerUID, check.UID)
	r.NotNil(job)
	_, hasKey := job.Config[checkerdef.SMTPResolvedRecipientConfigKey]
	r.False(hasKey, "a non-send-mode job must never carry a resolved recipient")
}

// TestResolveSMTPDelivery_CrossOrgRejected pins the org-scoping guarantee at
// the resolution boundary (not just at write time): a delivery_check_uid
// naming an email check in ANOTHER org must be refused — the same-org lookup
// simply cannot find it — with a loud error result, never a silent skip.
func TestResolveSMTPDelivery_CrossOrgRejected(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	be, _, dbSvc, ctx := newDirectBackend(t)

	otherOrg := newOrg(t, ctx, dbSvc, "smtp-other-org")
	foreignEmailCheck := seedEmailCheck(
		ctx, t, dbSvc, otherOrg, "foreign-delivery", "f00d000000000000000000000000000000000000000011",
	)

	org := newOrg(t, ctx, dbSvc, "smtp-cross-org")
	configureEmailInbox(ctx, t, dbSvc)
	smtpCheck := seedSMTPJob(ctx, t, dbSvc, org, "probe", sendModeConfig(foreignEmailCheck.UID))

	workerUID := registerWorker(ctx, t, dbSvc, "wk-smtp-crossorg")

	r.Nil(claimOne(ctx, t, be, workerUID, smtpCheck.UID),
		"a cross-org delivery_check_uid must never be dispatched")

	assertSMTPErrorResult(ctx, t, dbSvc, org.UID, smtpCheck.UID)
}

// TestResolveSMTPDelivery_WrongTypeRejected pins the CheckTypeEmail-only rule:
// a delivery_check_uid naming a same-org check of a different type is refused.
func TestResolveSMTPDelivery_WrongTypeRejected(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	be, _, dbSvc, ctx := newDirectBackend(t)

	org := newOrg(t, ctx, dbSvc, "smtp-wrong-type")
	configureEmailInbox(ctx, t, dbSvc)

	notEmail := models.NewCheck(org.UID, "not-email", "http")
	require.NoError(t, dbSvc.CreateCheck(ctx, notEmail))

	smtpCheck := seedSMTPJob(ctx, t, dbSvc, org, "probe", sendModeConfig(notEmail.UID))
	workerUID := registerWorker(ctx, t, dbSvc, "wk-smtp-wrongtype")

	r.Nil(claimOne(ctx, t, be, workerUID, smtpCheck.UID),
		"a delivery_check_uid naming a non-email check must never be dispatched")

	assertSMTPErrorResult(ctx, t, dbSvc, org.UID, smtpCheck.UID)
}

// TestResolveSMTPDelivery_DeletedReference pins "deleted referenced check
// fails loudly, never silently skips": a delivery_check_uid that never
// existed (as good as deleted from the resolver's point of view — GetCheck
// returns not-found either way) must produce an explicit error result.
func TestResolveSMTPDelivery_DeletedReference(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	be, _, dbSvc, ctx := newDirectBackend(t)

	org := newOrg(t, ctx, dbSvc, "smtp-deleted-ref")
	configureEmailInbox(ctx, t, dbSvc)

	smtpCheck := seedSMTPJob(ctx, t, dbSvc, org, "probe", sendModeConfig("00000000-0000-0000-0000-000000000000"))
	workerUID := registerWorker(ctx, t, dbSvc, "wk-smtp-deletedref")

	r.Nil(claimOne(ctx, t, be, workerUID, smtpCheck.UID),
		"a dangling delivery_check_uid must never be dispatched")

	assertSMTPErrorResult(ctx, t, dbSvc, org.UID, smtpCheck.UID)
}

// TestResolveSMTPDelivery_NoEmailInboxConfigured pins the instance-wide
// prerequisite: with no email.inbox system parameter at all, send mode cannot
// resolve any recipient regardless of how valid the reference is.
func TestResolveSMTPDelivery_NoEmailInboxConfigured(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	be, _, dbSvc, ctx := newDirectBackend(t)

	org := newOrg(t, ctx, dbSvc, "smtp-no-inbox")
	emailCheck := seedEmailCheck(ctx, t, dbSvc, org, "delivery", "abc123000000000000000000000000000000000000000f")
	smtpCheck := seedSMTPJob(ctx, t, dbSvc, org, "probe", sendModeConfig(emailCheck.UID))

	workerUID := registerWorker(ctx, t, dbSvc, "wk-smtp-noinbox")

	r.Nil(claimOne(ctx, t, be, workerUID, smtpCheck.UID),
		"send mode must not dispatch with no email inbox configured")

	assertSMTPErrorResult(ctx, t, dbSvc, org.UID, smtpCheck.UID)
}

// assertSMTPErrorResult asserts a StatusError result landed for checkUID
// (the "loud error, never a silent skip" contract) and that the job's lease
// was released so it stays reschedulable.
func assertSMTPErrorResult(ctx context.Context, t *testing.T, dbSvc db.Service, orgUID, checkUID string) {
	t.Helper()
	r := require.New(t)

	results, err := dbSvc.GetLastResultForChecks(ctx, orgUID, []string{checkUID})
	r.NoError(err)

	got, ok := results[checkUID]
	r.True(ok, "an unresolvable send-mode job must still produce a visible result")
	r.NotNil(got.Status)
	r.Equal(int(models.ResultStatusError), *got.Status)
	r.NotEmpty(got.Output["error"])

	var job models.CheckJob
	r.NoError(dbSvc.DB().NewSelect().Model(&job).Where("check_uid = ?", checkUID).Scan(ctx))
	r.Nil(job.LeaseWorkerUID, "the lease must be released so the job is reschedulable")
}
