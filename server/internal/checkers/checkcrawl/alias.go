package checkcrawl

import crawlconfig "github.com/fclairamb/solidping/server/internal/checkers/checkcrawl/config"

// CrawlConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps the `checkcrawl.CrawlConfig` name.
type CrawlConfig = crawlconfig.CrawlConfig

// Finding types, aliased so this package keeps the short names.
const (
	findingBrokenLink          = crawlconfig.FindingBrokenLink
	findingBrokenExternalLink  = crawlconfig.FindingBrokenExternalLink
	findingMixedContentActive  = crawlconfig.FindingMixedContentActive
	findingMixedContentPassive = crawlconfig.FindingMixedContentPassive
	findingSitemapError        = crawlconfig.FindingSitemapError
)
