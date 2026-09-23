package config

import "time"

// Bounds and defaults the config's own rules are expressed in. They live with
// the config so an offline validator can apply them without linking the
// checker's execution client.
const (
	DefaultTimeout  = 5 * time.Second
	DefaultCount    = 1
	DefaultInterval = 1 * time.Second
	MinCount        = 1
	MaxCount        = 600
	MinInterval     = 10 * time.Millisecond
	MaxInterval     = 60 * time.Second
)
