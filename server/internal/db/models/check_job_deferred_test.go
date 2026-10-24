package models_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

func TestCheckJobRateLimitDeferred(t *testing.T) {
	t.Parallel()

	scheduled := time.Date(2026, 10, 19, 8, 35, 46, 0, time.UTC)
	at := func(d time.Duration) *time.Time {
		v := scheduled.Add(d)

		return &v
	}

	cases := []struct {
		name      string
		period    time.Duration
		effective *time.Time
		want      bool
	}{
		{name: "on time", period: time.Hour, effective: at(0), want: false},
		{name: "slow-check offset", period: time.Minute, effective: at(30 * time.Second), want: false},
		{name: "paid tier credit", period: time.Hour, effective: at(-10 * time.Second), want: false},
		{name: "deferred once", period: time.Hour, effective: at(-time.Hour), want: true},
		{name: "deferred for a day", period: time.Hour, effective: at(-31 * time.Hour), want: true},
		{name: "one-minute deferred with offset", period: time.Minute, effective: at(-30 * time.Second), want: true},
		{name: "no effective schedule", period: time.Hour, effective: nil, want: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			job := &models.CheckJob{
				Period:               timeutils.Duration(testCase.period),
				ScheduledAt:          &scheduled,
				EffectiveScheduledAt: testCase.effective,
			}
			require.Equal(t, testCase.want, job.RateLimitDeferred())
		})
	}
}
