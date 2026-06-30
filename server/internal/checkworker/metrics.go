package checkworker

import (
	"github.com/prometheus/client_golang/prometheus"
)

// workerChannelCollector samples the length of a worker's jobsChan at scrape
// time. Since jobsChan is unbuffered, len() is always 0 in normal operation;
// the metric is still useful as infrastructure for future buffered channels and
// as a sanity check (non-zero would indicate a bug).
type workerChannelCollector struct {
	worker    *CheckWorker
	depthDesc *prometheus.Desc
}

func newWorkerChannelCollector(checkWorker *CheckWorker) prometheus.Collector {
	return &workerChannelCollector{
		worker: checkWorker,
		depthDesc: prometheus.NewDesc(
			"solidping_worker_jobs_channel_depth",
			"Number of jobs currently buffered in the worker's jobsChan (sampled at scrape time)",
			nil,
			prometheus.Labels{"worker_uid": checkWorker.getWorker().UID},
		),
	}
}

func (c *workerChannelCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.depthDesc
}

func (c *workerChannelCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(
		c.depthDesc,
		prometheus.GaugeValue,
		float64(len(c.worker.jobsChan)),
	)
}
