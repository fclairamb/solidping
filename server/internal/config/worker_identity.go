package config

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
)

// WorkerSlugPattern mirrors the `workers.slug` CHECK constraint as last
// rewritten in server/internal/db/postgres/migrations/018_v0_24_0.up.sql:
//
//	check (slug ~ '^[a-z0-9][a-z0-9-]{2,20}$')
//
// Keeping the two in sync is what lets a bad worker identity fail at startup
// with an actionable message instead of as an opaque SQLSTATE=23514 on INSERT.
//
// A leading digit is allowed on purpose: Docker's default hostname is the
// hex container ID, which starts with a digit ten times out of sixteen, and
// the original leading-letter rule made `docker run` a coin flip. The slug is
// an opaque upsert key, never a DNS label or an identifier, so nothing needed
// the letter (spec 2026-09-05-04).
const WorkerSlugPattern = `^[a-z0-9][a-z0-9-]{2,20}$`

// WorkerHostnameMaxLen is how much of the OS hostname is kept when no explicit
// SP_NODE_NAME override is configured. Historic value: the slug column allows
// 21 characters, and this cut has been in place since the first release, so it
// is preserved verbatim to keep existing deployments registering under the very
// same slug.
const WorkerHostnameMaxLen = 15

var workerSlugRegexp = regexp.MustCompile(WorkerSlugPattern)

// nonSlugCharRegexp matches any character illegal in a worker slug. It is used
// to substitute (never strip) the offending run of characters with a single
// dash — see sanitizeHostnameSlug.
var nonSlugCharRegexp = regexp.MustCompile(`[^a-z0-9-]+`)

// dashRunRegexp collapses runs of dashes produced by nonSlugCharRegexp
// substitutions (or already present in the hostname) into a single dash.
var dashRunRegexp = regexp.MustCompile(`-+`)

// unknownHostname is the placeholder used when os.Hostname() fails. Historic
// value, preserved so the derived slug is unchanged.
const unknownHostname = "unknown"

// ErrInvalidWorkerSlug is returned when the effective worker slug (override or
// hostname-derived) cannot satisfy the database CHECK constraint.
var ErrInvalidWorkerSlug = errors.New("invalid worker slug")

// osHostname is indirected so tests can drive the hostname-derived path
// deterministically.
//
//nolint:gochecknoglobals // test seam, never mutated outside _test.go files
var osHostname = os.Hostname

// WorkerIdentity is the slug/name pair a `solidping` process registers its
// `workers` row under. It is resolved once from the configuration and shared by
// the check worker and the job worker so both agree on who they are.
type WorkerIdentity struct {
	// Slug is the value stored in workers.slug (registration is upsert-by-slug).
	Slug string
	// Name is the human-readable workers.name.
	Name string
	// Overridden reports whether the identity came from SP_NODE_NAME rather than
	// the OS hostname.
	Overridden bool
	// Hostname is the raw, untruncated OS hostname the identity was derived
	// from. Empty when Overridden.
	Hostname string
	// Truncated reports that the hostname was longer than WorkerHostnameMaxLen
	// and got cut — which silently collapses two workers onto one row when their
	// hostnames share a prefix.
	Truncated bool
	// Sanitized reports that the (lowercased, truncated) hostname contained
	// characters outside [a-z0-9-] and had to be substituted to build Slug —
	// which silently collapses two workers onto one row when their hostnames
	// only differ by such characters (e.g. "host-002.lan" and "host-002-lan").
	Sanitized bool
}

// WorkerIdentity resolves this node's worker identity: the SP_NODE_NAME
// override when set, otherwise the (lowercased, truncated) OS hostname.
func (c *Config) WorkerIdentity() WorkerIdentity {
	return resolveWorkerIdentity(c.Node.Name, osHostname)
}

// resolveWorkerIdentity is the testable core of Config.WorkerIdentity.
func resolveWorkerIdentity(override string, hostnameFn func() (string, error)) WorkerIdentity {
	if override != "" {
		// Verbatim: the hostname is never consulted, and no truncation or case
		// folding is applied — what the operator configured is what registers.
		return WorkerIdentity{Slug: override, Name: override, Overridden: true}
	}

	hostname, err := hostnameFn()
	if err != nil {
		hostname = unknownHostname
	}

	raw := hostname
	truncated := false

	if len(hostname) > WorkerHostnameMaxLen {
		hostname = hostname[:WorkerHostnameMaxLen]
		truncated = true
	}

	lowered := strings.ToLower(hostname)
	slug := sanitizeHostnameSlug(lowered)

	return WorkerIdentity{
		Slug:      slug,
		Name:      hostname,
		Hostname:  raw,
		Truncated: truncated,
		Sanitized: slug != lowered,
	}
}

// sanitizeHostnameSlug turns a lowercased, length-truncated hostname into a
// value that stands a chance of matching WorkerSlugPattern: every run of
// characters outside [a-z0-9-] is substituted with a single dash (never
// stripped — "host-002.lan" becomes "host-002-lan", not "host-002lan", which
// is what an operator reading the slug would predict), then runs of dashes
// are collapsed and the result is trimmed of leading/trailing dashes (avoids
// "host--002" and dangling dashes left by truncation).
//
// A hostname that already matches WorkerSlugPattern short-circuits and is
// returned untouched. This is not just an optimization: WorkerSlugPattern
// permits trailing dashes and internal dash runs (e.g. a 15-char truncation
// landing mid-word as "my-worker-node-", or a genuine "db--primary--x1"),
// so collapsing/trimming unconditionally would rewrite slugs that already
// validate and are already registered today — silently moving an existing
// deployment's `workers` row. The collapse/trim steps below exist only to
// clean up residue *introduced by substitution* (a run of illegal characters
// becoming a run of dashes, or truncation landing right after one), so they
// must never run on a hostname that never needed substitution in the first
// place. The pathological residue that remains after substitution (shorter
// than 3 chars, or empty after collapsing) is left for Validate() to reject
// with its existing actionable error — sanitizing is not a promise that the
// result is valid. A leading digit is not residue: WorkerSlugPattern admits
// it, so "10.0.0.1" sanitizes to a perfectly valid "10-0-0-1".
func sanitizeHostnameSlug(hostname string) string {
	if workerSlugRegexp.MatchString(hostname) {
		return hostname
	}

	sanitized := nonSlugCharRegexp.ReplaceAllString(hostname, "-")
	sanitized = dashRunRegexp.ReplaceAllString(sanitized, "-")

	return strings.Trim(sanitized, "-")
}

// Validate checks the effective slug against the database CHECK constraint and
// returns an actionable error naming SP_NODE_NAME as the fix.
func (i WorkerIdentity) Validate() error {
	if workerSlugRegexp.MatchString(i.Slug) {
		return nil
	}

	if i.Overridden {
		return fmt.Errorf(
			"%w: SP_NODE_NAME=%q does not match %s",
			ErrInvalidWorkerSlug, i.Slug, WorkerSlugPattern,
		)
	}

	return fmt.Errorf(
		"%w: %q (derived from hostname %q) does not match %s — set SP_NODE_NAME to an explicit worker name",
		ErrInvalidWorkerSlug, i.Slug, i.Hostname, WorkerSlugPattern,
	)
}

// WarnIfTruncated logs a WARN when the identity came from a hostname long
// enough to be cut. Two hosts sharing the first WorkerHostnameMaxLen characters
// (Kubernetes pod names routinely do) otherwise collapse onto a single
// upsert-by-slug `workers` row and silently fight over it.
func (i WorkerIdentity) WarnIfTruncated(ctx context.Context, logger *slog.Logger) {
	if !i.Truncated || logger == nil {
		return
	}

	logger.WarnContext(ctx,
		"Worker hostname truncated to build the worker slug; hosts sharing this "+
			"prefix collapse onto the same workers row — set SP_NODE_NAME to pin an identity",
		"hostname", i.Hostname,
		"slug", i.Slug,
		"maxLength", WorkerHostnameMaxLen,
	)
}

// WarnIfSanitized logs a WARN when the hostname contained characters outside
// [a-z0-9-] and had to be substituted to build the worker slug. Two hosts
// that only differ by such characters (e.g. "host-002.lan" and
// "host-002-lan") otherwise collapse onto a single upsert-by-slug `workers`
// row and silently fight over it.
func (i WorkerIdentity) WarnIfSanitized(ctx context.Context, logger *slog.Logger) {
	if !i.Sanitized || logger == nil {
		return
	}

	logger.WarnContext(ctx,
		"Worker hostname contained characters outside the worker slug alphabet "+
			"and was sanitized; hosts that only differ by such characters collapse "+
			"onto the same workers row — set SP_NODE_NAME to pin an identity",
		"hostname", i.Hostname,
		"slug", i.Slug,
	)
}
