package archive_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/archive"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
)

func TestFileIDLayout(t *testing.T) {
	t.Parallel()

	const check = "0a1b2c3d-0000-4000-8000-000000000001"

	tests := []struct {
		name   string
		period string
		start  time.Time
		want   string
	}{
		{
			name: "hour", period: models.PeriodTypeHour,
			start: time.Date(2026, 11, 25, 14, 0, 0, 0, time.UTC),
			want:  check + "/2026-11-25/14.vortex",
		},
		{
			name: "last millisecond of a day stays on that day", period: models.PeriodTypeHour,
			start: time.Date(2026, 11, 25, 23, 59, 59, 999_000_000, time.UTC),
			want:  check + "/2026-11-25/23.vortex",
		},
		{
			name: "midnight is the next day", period: models.PeriodTypeHour,
			start: time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC),
			want:  check + "/2026-11-26/00.vortex",
		},
		{
			name: "a non-UTC start is converted to UTC", period: models.PeriodTypeHour,
			start: time.Date(2026, 11, 26, 0, 30, 0, 0, time.FixedZone("CET", 3600)),
			want:  check + "/2026-11-25/23.vortex",
		},
		{
			name: "day", period: models.PeriodTypeDay,
			start: time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC),
			want:  check + "/2026-11-25.vortex",
		},
		{
			name: "month rollover", period: models.PeriodTypeDay,
			start: time.Date(2026, 12, 31, 23, 59, 59, 999_000_000, time.UTC).Add(time.Millisecond),
			want:  check + "/2027-01-01.vortex",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := archive.FileID(check, tc.period, tc.start)
			require.True(t, ok)
			require.Equal(t, tc.want, got)
		})
	}

	_, ok := archive.FileID(check, models.PeriodTypeMonth, time.Now())
	require.False(t, ok, "the day to month compaction is not archived")
}

func TestFileIDRoundTripThroughStoragePath(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	orgUID := uuid.New()
	fileID := archive.HourFileID("check-1", time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC))

	path := filestorage.BuildPath(orgUID, filestorage.GroupTypeArchives, fileID)
	r.Equal(orgUID.String()+"/archives/check-1/2026-11-25/09.vortex", path)

	gotOrg, gotGroup, gotFileID, err := filestorage.ParsePath(path)
	r.NoError(err)
	r.Equal(orgUID, gotOrg)
	r.Equal(filestorage.GroupTypeArchives, gotGroup)
	r.Equal(fileID, gotFileID)
}
