package app

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/uptimereport"
)

// uptimeReportMailer adapts the job queue to reportschedules.Mailer, so the
// handler package never depends on the jobs packages and the "send me one now"
// path enqueues through exactly the same helper the scheduled sweep uses.
type uptimeReportMailer struct {
	services *services.Registry
}

// SendReport enqueues one rendered report email.
func (m *uptimeReportMailer) SendReport(
	ctx context.Context, orgUID, recipient string, data *uptimereport.Data, unsubscribeURL string,
) error {
	return jobtypes.EnqueueUptimeReportEmail(
		ctx, m.services, orgUID, recipient, data, unsubscribeURL,
	)
}
