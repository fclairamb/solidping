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

// WorkerSlugPattern mirrors the `workers.slug` CHECK constraint declared in
// server/internal/db/postgres/migrations/001_v0_1_0.up.sql:
//
//	slug text not null check (slug ~ '^[a-z][a-z0-9-]{2,20}$')
//
// Keeping the two in sync is what lets a bad worker identity fail at startup
// with an actionable message instead of as an opaque SQLSTATE=23514 on INSERT.
const WorkerSlugPattern = `^[a-z][a-z0-9-]{2,20}$`

// WorkerHostnameMaxLen is how much of the OS hostname is kept when no explicit
// SP_NODE_NAME override is configured. Historic value: the slug column allows
// 21 characters, and this cut has been in place since the first release, so it
// is preserved verbatim to keep existing deployments registering under the very
// same slug.
const WorkerHostnameMaxLen = 15

var workerSlugRegexp = regexp.MustCompile(WorkerSlugPattern)

// ErrInvalidWorkerSlug is returned when the effective worker slug (override or
// hostname-derived) cannot satisfy the database CHECK constraint.
var ErrInvalidWorkerSlug = errors.New("invalid worker slug")

// osHostname is indirected so tests can drive the hostname-derived path
// deterministically.
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
		hostname = "unknown"
	}

	raw := hostname
	truncated := false

	if len(hostname) > WorkerHostnameMaxLen {
		hostname = hostname[:WorkerHostnameMaxLen]
		truncated = true
	}

	return WorkerIdentity{
		Slug:      strings.ToLower(hostname),
		Name:      hostname,
		Hostname:  raw,
		Truncated: truncated,
	}
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
		"Worker hostname truncated to build the worker slug; hosts sharing this prefix will collapse onto the same workers row — set SP_NODE_NAME to pin an explicit identity",
		"hostname", i.Hostname,
		"slug", i.Slug,
		"maxLength", WorkerHostnameMaxLen,
	)
}
