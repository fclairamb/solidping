package checkprometheus

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkprometheus/config"

// PrometheusConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkprometheus.PrometheusConfig` call site compiling.
type PrometheusConfig = checkconfig.PrometheusConfig

// Defined with the config; aliased so this package keeps the short name.
const (
	MatchSingle      = checkconfig.MatchSingle
	MaxScrapeBytes   = checkconfig.MaxScrapeBytes
	ModePromQL       = checkconfig.ModePromQL
	OnMissingDown    = checkconfig.OnMissingDown
	OnMissingUp      = checkconfig.OnMissingUp
	OnMissingWarning = checkconfig.OnMissingWarning
	OpGreater        = checkconfig.OpGreater
)

// Defined with the config; aliased so this package keeps the short name.
const (
	MatchAvg       = checkconfig.MatchAvg
	MatchMax       = checkconfig.MatchMax
	MatchMin       = checkconfig.MatchMin
	MatchSum       = checkconfig.MatchSum
	ModeScrape     = checkconfig.ModeScrape
	OpEqual        = checkconfig.OpEqual
	OpGreaterEqual = checkconfig.OpGreaterEqual
	OpLess         = checkconfig.OpLess
	OpLessEqual    = checkconfig.OpLessEqual
	OpNotEqual     = checkconfig.OpNotEqual
)
