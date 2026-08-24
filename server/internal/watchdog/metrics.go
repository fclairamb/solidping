package watchdog

import (
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// allDetectors is the fixed detector vocabulary, used to publish an explicit
// zero for every detector/severity pair on every run.
//
// Publishing zeros matters: a Prometheus alert written as
// `solidping_watchdog_anomalies{detector="dark-region"} > 0` needs the series
// to EXIST while things are healthy, otherwise the alert silently evaluates
// against no data and an operator reads "no alert" as "all good" — which is
// precisely the confusion this whole spec is about.
//
//nolint:gochecknoglobals // static vocabulary, treated as a constant
var allDetectors = []string{DetectorDarkRegion, DetectorFleetCollapse, DetectorStaleIncidents}

//nolint:gochecknoglobals // static vocabulary, treated as a constant
var allSeverities = []Severity{SeverityInfo, SeverityWarning, SeverityCritical}

// PublishMetrics exports the run's findings on the Prometheus registry.
//
// It publishes EVERY anomaly, not just the ones past minSeverity: the
// out-of-band path exists so an external Prometheus can alert on its own
// thresholds, independently of what the in-band digest was configured to
// deliver.
func PublishMetrics(report *Report) {
	counts := make(map[string]map[Severity]int, len(allDetectors))
	for _, detector := range allDetectors {
		counts[detector] = make(map[Severity]int, len(allSeverities))
	}

	for _, anomaly := range report.Anomalies {
		if _, known := counts[anomaly.Detector]; !known {
			counts[anomaly.Detector] = make(map[Severity]int, len(allSeverities))
		}

		counts[anomaly.Detector][anomaly.Severity]++
	}

	for detector, bySeverity := range counts {
		// A detector that ERRORED knows nothing this run. Writing a 0 for it
		// would publish "healthy" as a fact, which is the same lie the whole
		// spec exists to prevent — so its gauge is left at its previous value
		// and the failure counter below carries the news instead.
		if !report.DetectorSucceeded(detector) {
			continue
		}

		for _, severity := range allSeverities {
			prommetrics.WatchdogAnomalies.
				WithLabelValues(detector, severity.String()).
				Set(float64(bySeverity[severity]))
		}
	}

	if report.DetectorSucceeded(DetectorDarkRegion) {
		prommetrics.WatchdogStrandedJobs.Set(float64(report.StrandedJobs))
	}

	if report.DetectorSucceeded(DetectorStaleIncidents) {
		prommetrics.WatchdogStaleIncidents.Set(float64(report.StaleIncidents))
	}

	for detector := range report.Failed {
		prommetrics.WatchdogDetectorFailures.WithLabelValues(detector).Inc()
	}

	prommetrics.WatchdogLastRun.Set(float64(report.GeneratedAt.Unix()))
}
