package config

import "time"

// Bounds and defaults the config's own rules are expressed in. They live with
// the config so an offline validator can apply them without linking the
// checker's execution client.
const (
	MaxTimeout = 30 * time.Second
)
