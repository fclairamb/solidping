package config

// Bounds and defaults the config's own rules are expressed in. They live with
// the config so an offline validator can apply them without linking the
// checker's execution client.
const (
	MaxSleepMs    = 120000
	SlugMaxLen    = 50
	StatusUp      = "up"
	StatusDown    = "down"
	StatusTimeout = "timeout"
	StatusError   = "error"
)
