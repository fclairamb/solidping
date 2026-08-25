package jobworker

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

// TestResolveWorkerSlug proves the job worker resolves its identity through the
// same shared helper as the check worker: SP_NODE_NAME wins verbatim, and the
// hostname fallback (plus its truncation/sanitization WARNs) is unchanged
// otherwise. The "hostname fallback" case runs against this machine's real OS
// hostname, so it must tolerate either WARN firing (e.g. this repo's dev
// laptops routinely have a dotted .lan hostname, which now sanitizes instead
// of failing startup) rather than asserting silence outright.
func TestResolveWorkerSlug(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		nodeName string
		wantSlug string
	}{
		{name: "override wins", nodeName: "solidping-eu2", wantSlug: "solidping-eu2"},
		{name: "hostname fallback"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			cfg := &config.Config{}
			cfg.Node.Name = tc.nodeName

			var buf bytes.Buffer
			worker := &JobWorker{
				config: cfg,
				logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})),
			}

			worker.resolveWorkerSlug(context.Background())

			identity := cfg.WorkerIdentity()
			want := tc.wantSlug
			if want == "" {
				want = identity.Slug
			}

			r.Equal(want, worker.workerSlug)
			// Same shared resolution as the check worker, by construction.
			r.Equal(identity.Slug, worker.workerSlug)

			if identity.Truncated || identity.Sanitized {
				r.Contains(buf.String(), "SP_NODE_NAME")
			} else {
				r.Empty(buf.String())
			}
		})
	}
}
