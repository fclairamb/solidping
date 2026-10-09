package jobtypes

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// Spec 2026-10-08-02: deleting a check resolves its active incidents in the DB
// layer. A queued escalation step for such an incident must page nobody, and
// the one "resolved, check deleted" notification must still be able to name
// the (now soft-deleted) check.

func TestEscalationStepStopsOnCheckDeletedIncident(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	f := newEscalationFixture(t, 2, nil)
	step := f.setStep(t, nil,
		models.NewEscalationPolicyTarget("", models.EscalationTargetConnection, uidPtr(f.channel.UID), 0),
		models.NewEscalationPolicyTarget("", models.EscalationTargetUser, uidPtr(f.user.UID), 1),
	)

	r.NoError(f.env.db.DeleteCheck(context.Background(), f.env.incident.CheckUID))

	r.NoError(f.run(step.UID, 0, true))
	r.Zero(f.env.emails.sends(), "nobody is paged for a deleted check's incident")
	r.Zero(f.jobCount(t, jobdef.JobTypeNotification))
	r.Zero(f.jobCount(t, jobdef.JobTypeEscalationStep), "no further cycle is queued")
	r.Empty(f.eventTypes(t))
}

func TestLoadNotificationCheckReadsDeletedCheckOnlyForCheckDeleted(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	env := setupPhoneEnv(t, false, "")
	r.NoError(env.db.DeleteCheck(ctx, env.incident.CheckUID))

	resolved, err := env.db.GetIncident(ctx, env.org.UID, env.incident.UID)
	r.NoError(err)
	r.Equal(models.ResolutionTypeCheckDeleted, *resolved.ResolutionType)

	check, err := loadNotificationCheck(ctx, env.jctx, env.org.UID, resolved)
	r.NoError(err, "the check_deleted notification still names the deleted check")
	r.Equal(env.incident.CheckUID, check.UID)

	// Any other incident on a deleted check keeps failing as before.
	other := *resolved
	auto := models.ResolutionTypeAuto
	other.ResolutionType = &auto
	_, err = loadNotificationCheck(ctx, env.jctx, env.org.UID, &other)
	r.Error(err)
}

func TestTelegramResolvedDetailSaysCheckDeleted(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	started := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	resolved := started.Add(42 * time.Minute)
	deleted := models.ResolutionTypeCheckDeleted

	r.Equal("closed because the check was deleted", telegramResolvedDetail(&models.Incident{
		StartedAt: started, ResolvedAt: &resolved, ResolutionType: &deleted,
	}))
	r.Equal("resolved after 42m0s", telegramResolvedDetail(&models.Incident{
		StartedAt: started, ResolvedAt: &resolved,
	}))
}
