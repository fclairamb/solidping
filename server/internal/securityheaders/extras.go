package securityheaders

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrInvalidExtraSource is returned for a headers.csp_extra_sources group that
// cannot be applied.
var ErrInvalidExtraSource = errors.New("invalid headers.csp_extra_sources entry")

// extensibleDirectives are the directives an operator may widen. Each extra
// source is appended to that directive on every surface whose policy sets it;
// a surface that does not restrict the directive is left alone (see
// policy.add). frame-ancestors is here so an operator can allow an intranet to
// frame every page at once, which also drops X-Frame-Options.
//
//nolint:gochecknoglobals // Effectively a constant set; Go has no const maps.
var extensibleDirectives = map[string]bool{
	dirDefault:        true,
	dirScript:         true,
	dirStyle:          true,
	dirImg:            true,
	dirFont:           true,
	dirConnect:        true,
	dirWorker:         true,
	"media-src":       true,
	"frame-src":       true,
	"manifest-src":    true,
	dirFrameAncestors: true,
}

// sourceRE is the shape of a source expression an operator may add: a quoted
// keyword or hash, a bare scheme ("https:"), or a host source with optional
// scheme, wildcard first label, port and path. No whitespace, `;` or `,` can
// match, so one entry can never close its directive and open another.
var sourceRE = regexp.MustCompile(
	`^(?:'(?:self|unsafe-inline|unsafe-eval|wasm-unsafe-eval|unsafe-hashes)'` +
		`|'sha(?:256|384|512)-[A-Za-z0-9+/_-]+={0,2}'` +
		`|[a-z][a-z0-9+.-]*:` +
		`|(?:[a-z][a-z0-9+.-]*://)?(?:\*|(?:\*\.)?[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?)(?::(?:[0-9]{1,5}|\*))?(?:/[A-Za-z0-9._~%!$&()*+=:@/-]*)?)$`,
)

// isSafeSource reports whether a single source expression can be put in a
// header verbatim.
func isSafeSource(source string) bool {
	return sourceRE.MatchString(source)
}

// ParseExtraSources reads headers.csp_extra_sources: `;`-separated groups,
// each a directive name followed by one or more sources, e.g.
//
//	img-src https://cdn.acme.com; connect-src https://sentry.acme.com
//
// Groups that do not parse are skipped and reported; the rest apply.
func ParseExtraSources(raw string) ([]directive, []error) {
	var (
		out  []directive
		errs []error
	)

	for _, group := range strings.Split(raw, ";") {
		fields := strings.Fields(group)
		if len(fields) == 0 {
			continue
		}

		name := strings.ToLower(fields[0])
		if !extensibleDirectives[name] {
			errs = append(errs, fmt.Errorf("%w: %q is not a directive that can be widened", ErrInvalidExtraSource, name))

			continue
		}

		if len(fields) == 1 {
			errs = append(errs, fmt.Errorf("%w: %q has no sources", ErrInvalidExtraSource, name))

			continue
		}

		sources := make([]string, 0, len(fields)-1)

		for _, source := range fields[1:] {
			// Keywords are case-insensitive in CSP; hashes are not.
			if !strings.HasPrefix(source, "'sha") {
				source = strings.ToLower(source)
			}

			if !isSafeSource(source) {
				errs = append(errs, fmt.Errorf("%w: %q is not a valid source for %s", ErrInvalidExtraSource, source, name))

				continue
			}

			sources = append(sources, source)
		}

		if len(sources) > 0 {
			out = append(out, directive{name: name, sources: sources})
		}
	}

	return out, errs
}
