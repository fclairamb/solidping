package config

// ResolveThresholds parses the four threshold keys, ignoring parse errors:
// Validate() is responsible for rejecting a bad value before a config is ever
// persisted, so a parse failure here would mean the config was never
// validated — treat the threshold as unset rather than panicking or failing
// the check for a config problem this code path cannot report cleanly.
func (c *RabbitMQConfig) ResolveThresholds() (*Threshold, *Threshold, *Threshold, *Threshold) {
	var memWarn, memCrit, diskWarn, diskCrit *Threshold

	if c.MemoryUsedWarning != "" {
		memWarn, _ = ParseThreshold(KeyMemoryUsedWarning, c.MemoryUsedWarning, true)
	}

	if c.MemoryUsedCritical != "" {
		memCrit, _ = ParseThreshold(KeyMemoryUsedCritical, c.MemoryUsedCritical, true)
	}

	if c.DiskFreeWarning != "" {
		diskWarn, _ = ParseThreshold(KeyDiskFreeWarning, c.DiskFreeWarning, false)
	}

	if c.DiskFreeCritical != "" {
		diskCrit, _ = ParseThreshold(KeyDiskFreeCritical, c.DiskFreeCritical, false)
	}

	return memWarn, memCrit, diskWarn, diskCrit
}
