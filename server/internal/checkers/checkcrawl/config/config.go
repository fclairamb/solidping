// Package config holds the crawl check's configuration (spec 2026-10-03-03):
// the struct, its map parsing and serialization, and the offline rule set
// (ValidateSpec). It is free of the crawler code so `sp checks validate` can
// run the server's own validators without linking it. The parent checkcrawl
// package keeps a type alias.
package config

import (
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Finding types (spec 2026-10-03-03 §2.4).
const (
	FindingBrokenLink          = "broken_link"
	FindingBrokenExternalLink  = "broken_external_link"
	FindingMixedContentActive  = "mixed_content_active"
	FindingMixedContentPassive = "mixed_content_passive"
	FindingSitemapError        = "sitemap_error"
)

// Sitemap modes. Anything else is an explicit sitemap URL.
const (
	SitemapAuto = "auto"
	SitemapOff  = "off"
)

// Defaults and bounds (spec 2026-10-03-03 §2.1).
const (
	DefaultMaxPages       = 200
	MaxMaxPages           = 2000
	DefaultConcurrency    = 2
	MaxConcurrency        = 4
	DefaultDelayMs        = 250
	MaxDelayMs            = 5000
	DefaultTimeout        = 10 * time.Second
	MinTimeout            = time.Second
	MaxTimeout            = 30 * time.Second
	DefaultMaxRunDuration = 30 * time.Minute
	MinMaxRunDuration     = 5 * time.Minute
	MaxMaxRunDuration     = 2 * time.Hour
	MaxPatterns           = 20

	keyURL                = "url"
	keyMaxPages           = "maxPages"
	keyCheckExternalLinks = "checkExternalLinks"
	keyCheckMixedContent  = "checkMixedContent"
	keySitemap            = "sitemap"
	keyRespectRobots      = "respectRobots"
	keyInclude            = "include"
	keyExclude            = "exclude"
	keyConcurrency        = "concurrency"
	keyDelayMs            = "delayMs"
	keyTimeout            = "timeout"
	keyMaxRunDuration     = "maxRunDuration"
	keyFailOn             = "failOn"
)

// FindingTypes lists every finding type, in report order.
func FindingTypes() []string {
	return []string{
		FindingBrokenLink, FindingBrokenExternalLink,
		FindingMixedContentActive, FindingMixedContentPassive, FindingSitemapError,
	}
}

// DefaultFailOn is the finding set that makes a run `down` by default.
// broken_external_link is deliberately absent (resolved open question 1).
func DefaultFailOn() []string {
	return []string{FindingBrokenLink, FindingMixedContentActive, FindingSitemapError}
}

// CrawlConfig is the configuration of a `crawl` check.
type CrawlConfig struct {
	URL                string        `json:"url"`
	MaxPages           int           `json:"maxPages,omitempty"`
	CheckExternalLinks *bool         `json:"checkExternalLinks,omitempty"`
	CheckMixedContent  *bool         `json:"checkMixedContent,omitempty"`
	Sitemap            string        `json:"sitemap,omitempty"`
	RespectRobots      *bool         `json:"respectRobots,omitempty"`
	Include            []string      `json:"include,omitempty"`
	Exclude            []string      `json:"exclude,omitempty"`
	Concurrency        int           `json:"concurrency,omitempty"`
	DelayMs            *int          `json:"delayMs,omitempty"`
	Timeout            time.Duration `json:"timeout,omitempty"`
	MaxRunDurationVal  time.Duration `json:"maxRunDuration,omitempty"`
	FailOn             []string      `json:"failOn,omitempty"`
}

// EffectiveMaxPages returns maxPages with its default applied.
func (c *CrawlConfig) EffectiveMaxPages() int {
	if c.MaxPages <= 0 {
		return DefaultMaxPages
	}

	return c.MaxPages
}

// ExternalLinks reports the effective checkExternalLinks (default true).
func (c *CrawlConfig) ExternalLinks() bool {
	return c.CheckExternalLinks == nil || *c.CheckExternalLinks
}

// MixedContent reports the effective checkMixedContent (default true).
func (c *CrawlConfig) MixedContent() bool {
	return c.CheckMixedContent == nil || *c.CheckMixedContent
}

// Robots reports the effective respectRobots (default true, resolved open
// question 3).
func (c *CrawlConfig) Robots() bool {
	return c.RespectRobots == nil || *c.RespectRobots
}

// SitemapMode returns the sitemap setting with its default applied.
func (c *CrawlConfig) SitemapMode() string {
	if c.Sitemap == "" {
		return SitemapAuto
	}

	return c.Sitemap
}

// EffectiveConcurrency returns concurrency with its default applied.
func (c *CrawlConfig) EffectiveConcurrency() int {
	if c.Concurrency <= 0 {
		return DefaultConcurrency
	}

	return c.Concurrency
}

// Delay returns the pause between requests with its default applied.
func (c *CrawlConfig) Delay() time.Duration {
	if c.DelayMs == nil {
		return DefaultDelayMs * time.Millisecond
	}

	return time.Duration(*c.DelayMs) * time.Millisecond
}

// RequestTimeout returns the per-request timeout with its default applied.
func (c *CrawlConfig) RequestTimeout() time.Duration {
	if c.Timeout <= 0 {
		return DefaultTimeout
	}

	return c.Timeout
}

// MaxRunDuration implements checkerdef.MaxRunDurationHint.
func (c *CrawlConfig) MaxRunDuration() time.Duration {
	if c.MaxRunDurationVal <= 0 {
		return DefaultMaxRunDuration
	}

	return c.MaxRunDurationVal
}

// UnitsPerRunHint implements checkerdef.UnitsPerRunHint: one page fetched is
// one unit, so a run costs up to maxPages executions.
func (c *CrawlConfig) UnitsPerRunHint() int {
	return c.EffectiveMaxPages()
}

// EffectiveFailOn returns failOn with its default applied.
func (c *CrawlConfig) EffectiveFailOn() []string {
	if len(c.FailOn) == 0 {
		return DefaultFailOn()
	}

	return c.FailOn
}

// FromMap populates the configuration from a map.
func (c *CrawlConfig) FromMap(configMap map[string]any) error {
	if err := c.readScalars(configMap); err != nil {
		return err
	}

	if err := c.readDurations(configMap); err != nil {
		return err
	}

	var err error

	if c.Include, err = readStrings(configMap, keyInclude); err != nil {
		return err
	}

	if c.Exclude, err = readStrings(configMap, keyExclude); err != nil {
		return err
	}

	c.FailOn, err = readStrings(configMap, keyFailOn)

	return err
}

func (c *CrawlConfig) readScalars(configMap map[string]any) error {
	var err error

	if c.URL, err = readString(configMap, keyURL); err != nil {
		return err
	}

	if c.Sitemap, err = readString(configMap, keySitemap); err != nil {
		return err
	}

	if c.CheckExternalLinks, err = readBool(configMap, keyCheckExternalLinks); err != nil {
		return err
	}

	if c.CheckMixedContent, err = readBool(configMap, keyCheckMixedContent); err != nil {
		return err
	}

	if c.RespectRobots, err = readBool(configMap, keyRespectRobots); err != nil {
		return err
	}

	if v, present, intErr := readInt(configMap, keyMaxPages); intErr != nil {
		return intErr
	} else if present {
		c.MaxPages = v
	}

	if v, present, intErr := readInt(configMap, keyConcurrency); intErr != nil {
		return intErr
	} else if present {
		c.Concurrency = v
	}

	if v, present, intErr := readInt(configMap, keyDelayMs); intErr != nil {
		return intErr
	} else if present {
		c.DelayMs = &v
	}

	return nil
}

func (c *CrawlConfig) readDurations(configMap map[string]any) error {
	var err error

	if c.Timeout, err = readDuration(configMap, keyTimeout); err != nil {
		return err
	}

	c.MaxRunDurationVal, err = readDuration(configMap, keyMaxRunDuration)

	return err
}

// GetConfig returns the configuration as a map. Defaults are omitted.
func (c *CrawlConfig) GetConfig() map[string]any {
	cfg := map[string]any{keyURL: c.URL}

	setIf := func(key string, value any, present bool) {
		if present {
			cfg[key] = value
		}
	}

	setIf(keyMaxPages, c.MaxPages, c.MaxPages != 0)
	setIf(keySitemap, c.Sitemap, c.Sitemap != "")
	setIf(keyConcurrency, c.Concurrency, c.Concurrency != 0)
	setIf(keyTimeout, c.Timeout.String(), c.Timeout != 0)
	setIf(keyMaxRunDuration, c.MaxRunDurationVal.String(), c.MaxRunDurationVal != 0)
	setIf(keyInclude, toAnySlice(c.Include), len(c.Include) > 0)
	setIf(keyExclude, toAnySlice(c.Exclude), len(c.Exclude) > 0)
	setIf(keyFailOn, toAnySlice(c.FailOn), len(c.FailOn) > 0)

	if c.CheckExternalLinks != nil {
		cfg[keyCheckExternalLinks] = *c.CheckExternalLinks
	}

	if c.CheckMixedContent != nil {
		cfg[keyCheckMixedContent] = *c.CheckMixedContent
	}

	if c.RespectRobots != nil {
		cfg[keyRespectRobots] = *c.RespectRobots
	}

	if c.DelayMs != nil {
		cfg[keyDelayMs] = *c.DelayMs
	}

	return cfg
}

// Validate applies every offline rule. Errors are *checkerdef.ConfigError.
func (c *CrawlConfig) Validate() error {
	if err := validateStartURL(c.URL); err != nil {
		return err
	}

	if err := c.validateBounds(); err != nil {
		return err
	}

	if err := validateSitemap(c.Sitemap); err != nil {
		return err
	}

	if len(c.Include) > MaxPatterns {
		return checkerdef.NewConfigErrorf(keyInclude, "at most %d patterns", MaxPatterns)
	}

	if len(c.Exclude) > MaxPatterns {
		return checkerdef.NewConfigErrorf(keyExclude, "at most %d patterns", MaxPatterns)
	}

	for _, findingType := range c.FailOn {
		if !slices.Contains(FindingTypes(), findingType) {
			return checkerdef.NewConfigErrorf(keyFailOn, "unknown finding type %q", findingType)
		}
	}

	return nil
}

func (c *CrawlConfig) validateBounds() error {
	if c.MaxPages < 0 || c.MaxPages > MaxMaxPages {
		return checkerdef.NewConfigErrorf(keyMaxPages, "must be between 1 and %d", MaxMaxPages)
	}

	if c.Concurrency < 0 || c.Concurrency > MaxConcurrency {
		return checkerdef.NewConfigErrorf(keyConcurrency, "must be between 1 and %d", MaxConcurrency)
	}

	if c.DelayMs != nil && (*c.DelayMs < 0 || *c.DelayMs > MaxDelayMs) {
		return checkerdef.NewConfigErrorf(keyDelayMs, "must be between 0 and %d", MaxDelayMs)
	}

	if c.Timeout != 0 && (c.Timeout < MinTimeout || c.Timeout > MaxTimeout) {
		return checkerdef.NewConfigError(keyTimeout, "must be between 1s and 30s")
	}

	if c.MaxRunDurationVal != 0 &&
		(c.MaxRunDurationVal < MinMaxRunDuration || c.MaxRunDurationVal > MaxMaxRunDuration) {
		return checkerdef.NewConfigError(keyMaxRunDuration, "must be between 5m and 2h")
	}

	return nil
}

// ValidateMaxPagesPresent refuses an explicit maxPages of 0. FromMap cannot
// tell "unset" from 0 once parsed, so the raw map is consulted.
func ValidateMaxPagesPresent(configMap map[string]any) error {
	if v, present, err := readInt(configMap, keyMaxPages); err == nil && present && v < 1 {
		return checkerdef.NewConfigErrorf(keyMaxPages, "must be between 1 and %d", MaxMaxPages)
	}

	return nil
}

func validateStartURL(raw string) error {
	if raw == "" {
		return checkerdef.NewConfigError(keyURL, "is required")
	}

	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return checkerdef.NewConfigError(keyURL, "must be an absolute http or https URL")
	}

	return nil
}

func validateSitemap(sitemap string) error {
	if sitemap == "" || sitemap == SitemapAuto || sitemap == SitemapOff {
		return nil
	}

	parsed, err := url.Parse(sitemap)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return checkerdef.NewConfigError(keySitemap, `must be "auto", "off" or an absolute http(s) URL`)
	}

	return nil
}

func readString(configMap map[string]any, key string) (string, error) {
	raw, present := configMap[key]
	if !present || raw == nil {
		return "", nil
	}

	value, ok := raw.(string)
	if !ok {
		return "", checkerdef.NewConfigError(key, "must be a string")
	}

	return strings.TrimSpace(value), nil
}

func readBool(configMap map[string]any, key string) (*bool, error) {
	raw, present := configMap[key]
	if !present || raw == nil {
		return nil, nil //nolint:nilnil // absent is a valid answer
	}

	value, ok := raw.(bool)
	if !ok {
		return nil, checkerdef.NewConfigError(key, "must be a boolean")
	}

	return &value, nil
}

func readInt(configMap map[string]any, key string) (int, bool, error) {
	switch value := configMap[key].(type) {
	case nil:
		return 0, false, nil
	case int:
		return value, true, nil
	case int64:
		return int(value), true, nil
	case float64:
		return int(value), true, nil
	default:
		return 0, false, checkerdef.NewConfigError(key, "must be a number")
	}
}

func readDuration(configMap map[string]any, key string) (time.Duration, error) {
	switch value := configMap[key].(type) {
	case nil:
		return 0, nil
	case string:
		if value == "" {
			return 0, nil
		}

		parsed, err := time.ParseDuration(value)
		if err != nil {
			return 0, checkerdef.NewConfigError(key, "must be a valid duration string")
		}

		return parsed, nil
	case time.Duration:
		return value, nil
	default:
		return 0, checkerdef.NewConfigError(key, "must be a duration string")
	}
}

func readStrings(configMap map[string]any, key string) ([]string, error) {
	switch value := configMap[key].(type) {
	case nil:
		return nil, nil
	case []string:
		return slices.Clone(value), nil
	case []any:
		out := make([]string, 0, len(value))

		for _, item := range value {
			text, ok := item.(string)
			if !ok {
				return nil, checkerdef.NewConfigError(key, "must be a list of strings")
			}

			out = append(out, text)
		}

		return out, nil
	default:
		return nil, checkerdef.NewConfigError(key, "must be a list of strings")
	}
}

func toAnySlice(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}

	return out
}
