// Package stats provides statistical tracking utilities.
package stats

import (
	"math"
	"time"
)

// EWMA implements Exponentially Weighted Moving Average for rate tracking.
type EWMA struct {
	value    float64
	alpha    float64
	interval time.Duration
	lastTime time.Time
}

// NewEWMA creates a new EWMA with the specified time interval.
func NewEWMA(interval time.Duration) EWMA {
	// alpha determines decay rate
	// For 1min: α ≈ 0.9200444146293232 (like Unix load average)
	alpha := 1 - math.Exp(-5.0/60.0/interval.Minutes())

	return EWMA{
		alpha:    alpha,
		interval: interval,
		lastTime: time.Now(),
	}
}

// Update records a new value and updates the moving average.
func (e *EWMA) Update(value float64) {
	now := time.Now()
	dt := now.Sub(e.lastTime)
	e.lastTime = now

	// Decay based on elapsed time
	decay := math.Exp(-dt.Seconds() / e.interval.Seconds())
	e.value = e.value*decay + value*(1-decay)
}

// Rate returns the current moving average value.
func (e EWMA) Rate() float64 {
	return e.value
}
