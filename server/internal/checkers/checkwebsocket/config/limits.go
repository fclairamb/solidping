package config

// Bounds and defaults the config's own rules are expressed in. They live with
// the config so an offline validator can apply them without linking the
// checker's execution client.
const (
	ConfigKeyURL = "url" // shared with checkconfig.go to avoid goconst
)
