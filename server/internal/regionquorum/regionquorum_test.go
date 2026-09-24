package regionquorum_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/regionquorum"
)

func TestParse(t *testing.T) {
	t.Parallel()

	cases := []struct {
		raw  string
		want regionquorum.Setting
		err  bool
	}{
		{raw: "", want: regionquorum.Setting{Kind: regionquorum.KindDefault}},
		{raw: "default", want: regionquorum.Setting{Kind: regionquorum.KindDefault}},
		{raw: "ALL", want: regionquorum.Setting{Kind: regionquorum.KindAll}},
		{raw: " majority ", want: regionquorum.Setting{Kind: regionquorum.KindMajority}},
		{raw: "2", want: regionquorum.Setting{Kind: regionquorum.KindCount, Count: 2}},
		{raw: "100", want: regionquorum.Setting{Kind: regionquorum.KindCount, Count: 100}},
		{raw: "0", err: true},
		{raw: "-1", err: true},
		{raw: "101", err: true},
		{raw: "2.5", err: true},
		{raw: "half", err: true},
		{raw: "true", err: true},
	}

	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			got, err := regionquorum.Parse(tc.raw)
			if tc.err {
				r.ErrorIs(err, regionquorum.ErrInvalid)

				return
			}

			r.NoError(err)
			r.Equal(tc.want, got)
		})
	}
}

func TestStoredRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Nil(regionquorum.Setting{Kind: regionquorum.KindDefault}.Stored())
	r.Equal(regionquorum.Setting{Kind: regionquorum.KindDefault}, regionquorum.FromStored(nil))

	for _, raw := range []string{"all", "majority", "3"} {
		setting, err := regionquorum.Parse(raw)
		r.NoError(err)
		r.Equal(raw, *setting.Stored())
		r.Equal(setting, regionquorum.FromStored(setting.Stored()))
	}

	garbage := "nonsense"
	r.Equal(regionquorum.Setting{Kind: regionquorum.KindDefault}, regionquorum.FromStored(&garbage))
}

func TestResolve(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	def := regionquorum.Setting{Kind: regionquorum.KindDefault}
	all := regionquorum.Setting{Kind: regionquorum.KindAll}
	maj := regionquorum.Setting{Kind: regionquorum.KindMajority}
	two := regionquorum.Setting{Kind: regionquorum.KindCount, Count: 2}

	// Default: all for N <= 2, majority for N >= 3.
	r.Equal(0, def.Resolve(0))
	r.Equal(1, def.Resolve(1))
	r.Equal(2, def.Resolve(2))
	r.Equal(2, def.Resolve(3))
	r.Equal(3, def.Resolve(4))
	r.Equal(3, def.Resolve(5))

	r.Equal(3, all.Resolve(3))
	r.Equal(2, maj.Resolve(2))
	r.Equal(3, maj.Resolve(4))

	// An integer is clamped to N.
	r.Equal(2, two.Resolve(4))
	r.Equal(1, two.Resolve(1))
}

func TestUsesQuorum(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	def := regionquorum.Setting{Kind: regionquorum.KindDefault}

	// The N <= 2 default keeps the per-result machine.
	for n := range 3 {
		r.False(regionquorum.UsesQuorum(def.Resolve(n), n), "n=%d", n)
	}

	r.True(regionquorum.UsesQuorum(def.Resolve(3), 3))
	// "all" never uses the quorum path, whatever N.
	all := regionquorum.Setting{Kind: regionquorum.KindAll}
	r.False(regionquorum.UsesQuorum(all.Resolve(5), 5))
	// An explicit 1 of 2 does: any single failing region counts.
	r.True(regionquorum.UsesQuorum(1, 2))
}

func TestForCheckPassive(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	check := &models.Check{Type: "heartbeat", Regions: []string{"a", "b", "c"}}
	_, n, q := regionquorum.ForCheck(check)
	r.Zero(n)
	r.Zero(q)

	check = &models.Check{Type: "http", Regions: []string{"a", "b", "b", "c"}}
	_, n, q = regionquorum.ForCheck(check)
	r.Equal(3, n)
	r.Equal(2, q)
}

func TestValueJSON(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var payload struct {
		FailQuorum *regionquorum.Value `json:"failQuorum,omitempty"`
	}

	for raw, want := range map[string]string{
		`{"failQuorum":2}`:          "2",
		`{"failQuorum":"2"}`:        "2",
		`{"failQuorum":"majority"}`: "majority",
		`{"failQuorum":true}`:       "true",
		`{"failQuorum":2.5}`:        "2.5",
	} {
		payload.FailQuorum = nil
		r.NoError(json.Unmarshal([]byte(raw), &payload), raw)
		r.NotNil(payload.FailQuorum, raw)
		r.Equal(want, string(*payload.FailQuorum), raw)
	}

	payload.FailQuorum = nil
	r.NoError(json.Unmarshal([]byte(`{"failQuorum":null}`), &payload))
	r.Nil(payload.FailQuorum)

	value := regionquorum.Value("3")
	payload.FailQuorum = &value
	out, err := json.Marshal(payload)
	r.NoError(err)
	r.JSONEq(`{"failQuorum":3}`, string(out))

	value = regionquorum.Value("majority")
	out, err = json.Marshal(payload)
	r.NoError(err)
	r.JSONEq(`{"failQuorum":"majority"}`, string(out))
}

func TestEvaluate(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-30 * time.Second)
	old := now.Add(-time.Hour)

	states := make([]regionquorum.RegionState, 0, 6)
	states = append(states,
		regionquorum.RegionState{Region: "a", Status: models.ResultStatusDown, LastResultAt: fresh},
		regionquorum.RegionState{Region: "b", Status: models.ResultStatusTimeout, LastResultAt: fresh},
		regionquorum.RegionState{Region: "c", Status: models.ResultStatusWarning, LastResultAt: fresh},
		// A stale failing reading does not count.
		regionquorum.RegionState{Region: "d", Status: models.ResultStatusError, LastResultAt: old},
		// A region the check has left does not count, whatever it says.
		regionquorum.RegionState{Region: "gone", Status: models.ResultStatusDown, LastResultAt: fresh},
	)

	eval := regionquorum.Evaluate([]string{"a", "b", "c", "d", "new"}, states, 3, now, 5*time.Minute)
	r.Equal(5, eval.Regions)
	r.Equal([]string{"a", "b"}, eval.Failing)
	r.Equal([]string{"c"}, eval.Passing)
	r.Equal([]string{"d", "new"}, eval.Unknown)
	r.False(eval.QuorumFailing())
	r.True(eval.RegionalIssue())

	eval = regionquorum.Evaluate([]string{"a", "b", "c"}, states, 2, now, 5*time.Minute)
	r.True(eval.QuorumFailing())
	r.False(eval.RegionalIssue())

	// Newest reading per region wins when a region appears twice.
	dup := append(states, regionquorum.RegionState{
		Region: "a", Status: models.ResultStatusUp, LastResultAt: now,
	})
	eval = regionquorum.Evaluate([]string{"a", "b", "c"}, dup, 2, now, 5*time.Minute)
	r.Equal([]string{"b"}, eval.Failing)
	r.True(eval.RegionalIssue())

	// Nothing failing: neither.
	eval = regionquorum.Evaluate([]string{"c"}, states, 1, now, 0)
	r.False(eval.QuorumFailing())
	r.False(eval.RegionalIssue())
}
