package config

// statusLabel returns the effective status label (defaulting to up).
func (c *SleepConfig) StatusLabel() string {
	if c.Status == "" {
		return StatusUp
	}

	return c.Status
}

// truncateSlug bounds a slug to SlugMaxLen runes.
func truncateSlug(s string) string {
	if len(s) <= SlugMaxLen {
		return s
	}

	return s[:SlugMaxLen]
}
