package prommetrics

// RecordExecution records a check execution's counter increment and duration observation.
// durationMs is provided in milliseconds and converted to seconds for the histogram.
func RecordExecution(checkType, status, region, org string, durationMs float64) {
	CheckExecutions.WithLabelValues(checkType, status, region, org).Inc()
	CheckDuration.WithLabelValues(checkType, status, region, org).Observe(durationMs / 1000)
}

// RecordSchedulingDelay records the delay between scheduled and actual execution time.
func RecordSchedulingDelay(region string, delaySeconds float64) {
	SchedulingDelay.WithLabelValues(region).Observe(delaySeconds)
}

// SetCheckStatus sets the up/down gauge for a specific check.
func SetCheckStatus(checkSlug, checkType, region, org string, up bool) {
	val := 0.0
	if up {
		val = 1.0
	}

	CheckUp.WithLabelValues(checkSlug, checkType, region, org).Set(val)
}

// SetCheckStatusStreak sets the consecutive status streak for a check.
func SetCheckStatusStreak(checkSlug, checkType, org string, streak float64) {
	CheckStatusStreak.WithLabelValues(checkSlug, checkType, org).Set(streak)
}

// SetChecksConfigured sets the number of configured checks for a given type/org/enabled combo.
func SetChecksConfigured(checkType, org, enabled string, count float64) {
	ChecksConfigured.WithLabelValues(checkType, org, enabled).Set(count)
}

// SetWorkersActive sets the number of active workers in a region.
func SetWorkersActive(region string, count float64) {
	WorkersActive.WithLabelValues(region).Set(count)
}

// SetWorkerFreeRunners sets the available runner slots for a worker.
func SetWorkerFreeRunners(workerUID, region string, count float64) {
	WorkerFreeRunners.WithLabelValues(workerUID, region).Set(count)
}

// RecordWorkerJobClaimed increments the jobs claimed counter for a worker.
func RecordWorkerJobClaimed(workerUID, region string) {
	WorkerJobsClaimed.WithLabelValues(workerUID, region).Inc()
}

// SetIncidentsActive sets the number of currently open incidents for an organization.
func SetIncidentsActive(org string, count float64) {
	IncidentsActive.WithLabelValues(org).Set(count)
}

// RecordIncidentCreated increments the total incidents counter.
func RecordIncidentCreated(org, checkType string) {
	IncidentsTotal.WithLabelValues(org, checkType).Inc()
}
