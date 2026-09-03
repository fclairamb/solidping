package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
	"github.com/fclairamb/solidping/server/internal/opsnotifywire"
)

// installOperatorNoticeDispatcher wires the process-wide hook the two notice
// raisers (`internal/support` and `handlers/auth`) call.
//
// The hook enqueues a job rather than sending inline. Both raisers are on a
// latency-critical path — a support webhook answering a provider under a
// deadline, and a signup completing — and neither may fail or slow down
// because a messaging provider is down. This mirrors analytics.Capture, and
// for the same reason: those packages sit UNDER app/services in the import
// graph, so the dependency has to arrive as a hook rather than as a field.
//
// The job is created with no organization: an operator notice is an INSTANCE
// event. The per-route org (whose Slack token or SMS sender carries it) is
// resolved later, from the recipient's own routes.
func (s *Server) installOperatorNoticeDispatcher() {
	jobSvc := s.jobSvc
	if jobSvc == nil {
		slog.Default().Warn(
			"No job service; operator notifications will not be delivered on this process")

		return
	}

	opsnotify.SetDispatcher(func(ctx context.Context, notice *opsnotify.Notice) error {
		payload, err := json.Marshal(jobtypes.OperatorNoticeJobConfig{
			Event:   notice.Event,
			Subject: notice.Subject,
			Body:    notice.Body,
			URL:     notice.URL,
		})
		if err != nil {
			return fmt.Errorf("encode operator notice: %w", err)
		}

		if _, err := jobSvc.CreateJob(ctx, "", string(jobdef.JobTypeOperatorNotice), payload, nil); err != nil {
			return fmt.Errorf("enqueue operator notice: %w", err)
		}

		return nil
	})
}

// operatorNoticeDeps builds the synchronous transport used by the
// "Send me a test" endpoint, which must report success or failure to the
// operator pressing the button rather than disappearing into the queue.
func (s *Server) operatorNoticeDeps() opsnotify.Deps {
	return opsnotifywire.Build(s.dbService, s.services, s.config)
}
