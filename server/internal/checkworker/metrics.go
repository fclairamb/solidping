package checkworker

import (
	"github.com/prometheus/client_golang/prometheus"
)

// workerChannelCollector samples per-worker scheduling gauges at scrape time:
// the length of the worker's jobsChan and the number of in-flight slow-lane
// jobs (busySlow). Since jobsChan is unbuffered, len() is always 0 in normal
// operation; the metric is still useful as infrastructure for future buffered
// channels and as a sanity check (non-zero would indicate a bug). busySlow is
// the live slow-lane occupancy against the reservation cap pool_size −
// fast_lane_reserved (spec 2026-07-01-03 D4): a value pinned at the cap means
// the slow lane is saturated and slow checks are degrading to best-effort.
type workerChannelCollector struct {
	worker       *CheckWorker
	depthDesc    *prometheus.Desc
	busySlowDesc *prometheus.Desc
}

func newWorkerChannelCollector(checkWorker *CheckWorker) prometheus.Collector {
	labels := prometheus.Labels{"worker_uid": checkWorker.getWorker().UID}

	return &workerChannelCollector{
		worker: checkWorker,
		depthDesc: prometheus.NewDesc(
			"solidping_worker_jobs_channel_depth",
			"Number of jobs currently buffered in the worker's jobsChan (sampled at scrape time)",
			nil,
			labels,
		),
		busySlowDesc: prometheus.NewDesc(
			"solidping_worker_busy_slow",
			"Number of slow-lane check jobs currently in flight on this worker (capped at pool_size − fast_lane_reserved)",
			nil,
			labels,
		),
	}
}

func (c *workerChannelCollector) Describe(descs chan<- *prometheus.Desc) {
	descs <- c.depthDesc
	descs <- c.busySlowDesc
}

func (c *workerChannelCollector) Collect(metrics chan<- prometheus.Metric) {
	metrics <- prometheus.MustNewConstMetric(
		c.depthDesc,
		prometheus.GaugeValue,
		float64(len(c.worker.jobsChan)),
	)
	metrics <- prometheus.MustNewConstMetric(
		c.busySlowDesc,
		prometheus.GaugeValue,
		float64(c.worker.busySlow.Load()),
	)
}
