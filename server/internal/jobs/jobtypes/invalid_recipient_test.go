package jobtypes

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/i18n/recipient"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/uptimereport"
)

// An email job with an unparseable recipient fails permanently, without ever
// reaching the sender, and logs no ERROR nor the address in clear.
func TestEmailJobRun_InvalidRecipientIsPermanent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	run := &EmailJobRun{config: EmailJobConfig{
		To: []string{"not-an-address"}, Subject: "s", Text: "x", Template: "",
	}}
	sender := &mockSender{}
	jctx := &jobdef.JobContext{
		Services: &services.Registry{EmailSender: sender},
		Logger:   slog.New(slog.NewTextHandler(&buf, nil)),
	}

	err := run.Run(context.Background(), jctx)

	require.ErrorIs(t, err, ErrEmailInvalidRecipient)
	require.True(t, jobdef.IsPermanent(err))
	require.False(t, jobdef.IsRetryable(err))
	require.NotContains(t, buf.String(), "level=ERROR")
	require.Contains(t, buf.String(), "level=WARN")
	require.NotContains(t, buf.String()+err.Error(), "not-an-address")
}

func TestEnqueueUptimeReportEmail_SkipsInvalidRecipient(t *testing.T) {
	t.Parallel()

	jobs := &capturingJobService{}
	registry := services.NewRegistry()
	registry.Jobs = jobs

	err := EnqueueUptimeReportEmail(t.Context(), registry, "org", "junk", recipientHint(), &uptimereport.Data{}, "")

	require.NoError(t, err)
	require.Empty(t, jobs.created)
}

func TestEnqueueCapNotice_SkipsInvalidRecipient(t *testing.T) {
	t.Parallel()

	jobs := &capturingJobService{}
	jctx := &jobdef.JobContext{Services: &services.Registry{Jobs: jobs}}

	enqueueCapNotice(t.Context(), jctx, slog.Default(), "org", &models.User{Email: "junk"}, nil)

	require.Empty(t, jobs.created)
}

func recipientHint() recipient.Hint { return recipient.Hint{} }
