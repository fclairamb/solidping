package stats

import (
	"log/slog"
	"sync"
	"time"
)

// ReportedStats contains the stats to be reported.
type ReportedStats struct {
	TotalChecks     int
	FailedChecks    int
	AverageDuration float64 // milliseconds
	AverageDelay    float64 // seconds
	FreeRunners     float64
	// Parked is the number of runner slots currently occupied by a claimed
	// job sleeping until its scheduled_at — claimed but not yet due (spec
	// 2026-07-05-08 D5). Visibility into how much of the pool the bounded
	// claim-ahead window (D3) is occupying at report time.
	Parked float64
}

// Reporter is a callback function that receives stats for reporting.
type Reporter func(stats ReportedStats)

// ProcessingStats tracks metrics for check execution performance.
type ProcessingStats struct {
	TotalChecks       int
	FailedChecks      int
	AverageDurationMs EWMA
	AverageDelayMs    EWMA
	logger            *slog.Logger
	mu                sync.Mutex
	lastCheck         time.Time
	reportingPeriod   time.Duration
	reporter          Reporter
	freeRunners       func() float64
	parked            func() float64
}

// NewProcessingStats creates a new ProcessingStats instance.
func NewProcessingStats(duration time.Duration, reportingPeriod time.Duration, logger *slog.Logger) ProcessingStats {
	return ProcessingStats{
		AverageDurationMs: NewEWMA(duration),
		AverageDelayMs:    NewEWMA(duration),
		logger:            logger,
		lastCheck:         time.Now(),
		reportingPeriod:   reportingPeriod,
	}
}

// SetReporter sets the callback function for reporting stats.
func (pm *ProcessingStats) SetReporter(reporter Reporter) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.reporter = reporter
}

// SetFreeRunnersFunc sets the callback to get current free runners count.
func (pm *ProcessingStats) SetFreeRunnersFunc(fn func() float64) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.freeRunners = fn
}

// SetParkedFunc sets the callback to get the current parked count (spec
// 2026-07-05-08 D5) — runner slots occupied by a claimed job sleeping until
// its scheduled_at.
func (pm *ProcessingStats) SetParkedFunc(fn func() float64) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.parked = fn
}

// AddMetric records a check execution result.
func (pm *ProcessingStats) AddMetric(success bool, duration time.Duration, delay time.Duration) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	pm.TotalChecks++
	if !success {
		pm.FailedChecks++
	}
	pm.AverageDurationMs.Update(float64(duration.Milliseconds()))
	pm.AverageDelayMs.Update(float64(delay.Milliseconds()))

	now := time.Now()
	if now.Sub(pm.lastCheck) > pm.reportingPeriod {
		pm.report()
		pm.lastCheck = now
	}
}

// report sends the current stats to the reporter and resets counters.
func (pm *ProcessingStats) report() {
	freeRunners := float64(0)
	if pm.freeRunners != nil {
		freeRunners = pm.freeRunners()
	}

	parked := float64(0)
	if pm.parked != nil {
		parked = pm.parked()
	}

	stats := ReportedStats{
		TotalChecks:     pm.TotalChecks,
		FailedChecks:    pm.FailedChecks,
		AverageDuration: pm.AverageDurationMs.Rate(),
		AverageDelay:    pm.AverageDelayMs.Rate(),
		FreeRunners:     freeRunners,
		Parked:          parked,
	}

	pm.logger.Info(
		"Processing stats",
		slog.Int("totalChecks", stats.TotalChecks),
		slog.Int("failedChecks", stats.FailedChecks),
		slog.Float64("averageDurationMs", stats.AverageDuration),
		slog.Float64("averageDelaySeconds", stats.AverageDelay),
		slog.Float64("parked", stats.Parked),
		slog.Float64("freeRunners", stats.FreeRunners),
	)

	if pm.reporter != nil {
		pm.reporter(stats)
	}

	// Reset counters after reporting
	pm.TotalChecks = 0
	pm.FailedChecks = 0
}
