package jobtypes

import "log/slog"

// levelForCount is the log level of a housekeeping job's result line: INFO
// when the run actually changed something, DEBUG when it found nothing to do.
// An idle instance must stay silent at INFO, but a purge that deleted rows is
// still worth seeing.
func levelForCount[T ~int | ~int64](count T) slog.Level {
	if count > 0 {
		return slog.LevelInfo
	}

	return slog.LevelDebug
}
