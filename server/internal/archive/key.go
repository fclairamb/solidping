package archive

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Extension is the suffix of every archive file.
const Extension = ".vortex"

// HourFileID is the fileID of the archive of the raw rows of one hour:
// <checkUID>/<YYYY-MM-DD>/<HH>.vortex, in UTC.
func HourFileID(checkUID string, start time.Time) string {
	utc := start.UTC()

	return checkUID + "/" + utc.Format(time.DateOnly) + "/" + utc.Format("15") + Extension
}

// DayFileID is the fileID of the long-term archive of one day, every raw row
// of the day (plus the hour rollups of the hours that had no raw row in the
// archive): <checkUID>/<YYYY-MM-DD>.vortex, in UTC.
func DayFileID(checkUID string, start time.Time) string {
	return checkUID + "/" + start.UTC().Format(time.DateOnly) + Extension
}

// HourDirPrefix is the DeletePrefix prefix of the hour files of one day:
// <checkUID>/<YYYY-MM-DD>/, in UTC.
func HourDirPrefix(checkUID string, day time.Time) string {
	return checkUID + "/" + day.UTC().Format(time.DateOnly) + "/"
}

// FileID returns the fileID of the archive written when a bucket is compacted
// into targetPeriod, and false for a tier that is not archived (month: the
// day rows it compacts are tiny and the month rows stay in the database).
func FileID(checkUID, targetPeriod string, start time.Time) (string, bool) {
	switch targetPeriod {
	case models.PeriodTypeHour:
		return HourFileID(checkUID, start), true
	case models.PeriodTypeDay:
		return DayFileID(checkUID, start), true
	default:
		return "", false
	}
}
