package checkfreeboxline_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkfreeboxline"
	"github.com/fclairamb/solidping/server/internal/egress"
)

// A freebox_line check whose box sits on a non-public address is refused under
// an enforcing egress policy; the same check under a permissive policy reaches
// the box (whatever the box then answers).
func TestExecuteRefusesAPrivateBoxUnderAnEnforcingPolicy(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var hits atomic.Int32

	box := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)

		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(box.Close)

	config := &checkfreeboxline.FreeboxLineConfig{}
	r.NoError(config.FromMap(map[string]any{"connectionUid": "conn-1", "linkType": "ftth"}))

	run := func(guard *egress.Guard) (*egress.DeniedError, string) {
		ctx := checkfreeboxline.WithResolver(egress.WithGuard(t.Context(), guard),
			func(context.Context, string) (*checkfreeboxline.ResolvedConnection, error) {
				return &checkfreeboxline.ResolvedConnection{BaseURL: box.URL, AppID: "a", AppToken: "t"}, nil
			})
		ctx, rec := egress.WithRecorder(ctx)

		result, err := (&checkfreeboxline.FreeboxLineChecker{}).Execute(ctx, config)
		r.NoError(err)

		return rec.Denied(), fmt.Sprint(result.Output)
	}

	denied, output := run(egress.New(false))
	r.NotNil(denied, output)
	r.Contains(output, "denied by egress policy")
	r.Equal(int32(0), hits.Load(), "the box was never contacted")

	allowed, output := run(egress.New(true))
	r.Nil(allowed, output)
	r.Positive(hits.Load())
}
