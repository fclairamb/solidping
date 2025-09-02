package stats

import (
	"fmt"
	"time"
)

// MetricTracker tracks metrics with 1, 5, and 15 minute moving averages.
type MetricTracker struct {
	oneMin     EWMA
	fiveMin    EWMA
	fifteenMin EWMA
}

// NewMetricTracker creates a new MetricTracker with 1, 5, and 15 minute windows.
func NewMetricTracker() MetricTracker {
	return MetricTracker{
		oneMin:     NewEWMA(1 * time.Minute),
		fiveMin:    NewEWMA(5 * time.Minute),
		fifteenMin: NewEWMA(15 * time.Minute),
	}
}

// Record adds a new value to all moving average windows.
func (m *MetricTracker) Record(value float64) {
	m.oneMin.Update(value)
	m.fiveMin.Update(value)
	m.fifteenMin.Update(value)
}

// GetRates returns the 1, 5, and 15 minute moving averages.
func (m *MetricTracker) GetRates() (float64, float64, float64) {
	return m.oneMin.Rate(), m.fiveMin.Rate(), m.fifteenMin.Rate()
}

func (m *MetricTracker) String() string {
	r1, r5, r15 := m.GetRates()
	return fmt.Sprintf("%.2f , %.2f , %.2f", r1, r5, r15)
}
