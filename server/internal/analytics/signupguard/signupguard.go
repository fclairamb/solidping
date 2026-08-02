// Package signupguard provides the source-level scan that keeps the
// user_signed_up product event honest (spec 2026-08-02-08).
//
// Account creation is the one event that can under-count *invisibly*: a new
// signup path calls db.CreateUser directly, nothing fails, no test goes red,
// and the metric is quietly wrong forever. The first cut of the analytics
// feature did exactly that — it fired only on the email/password confirmation
// path while every SSO provider and invite acceptance created accounts
// silently.
//
// So every package that can create a user account carries a test calling
// ScanDir, asserting that the only CreateUser call in it is the one that also
// captures the event. Keeping the scan here rather than duplicating it per
// package means a future signup path in a NEW package is one three-line test
// away from being covered.
//
// This package is test-support only; nothing in the production build imports it.
package signupguard

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// createUserCallRe matches a call to the DB's account-creating CreateUser,
// whatever its formatting.
//
// Deliberately NOT anchored to a single line and NOT tied to the first argument
// being named `ctx`: this repo's lll rule (120 chars) actively encourages
// wrapping long calls, so a wrapped or renamed-context call must not slip past.
// Matching on the method name plus the opening paren is what makes it immune to
// both. The trailing `\(` also keeps the non-account-creating siblings
// (CreateUserProvider, CreateUserToken, CreateUserPasskey) from matching.
var createUserCallRe = regexp.MustCompile(`\.CreateUser\(`)

// ScanDir returns "file:line" locations of every CreateUser call in the
// non-test .go files of dir, excluding the chokepoint files named in
// allowedFiles (the ones that create AND capture).
//
// A non-empty result means some signup path bypasses analytics capture.
func ScanDir(t *testing.T, dir string, allowedFiles ...string) []string {
	t.Helper()

	allowed := make(map[string]bool, len(allowedFiles))
	for _, name := range allowedFiles {
		allowed[name] = true
	}

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	var offenders []string

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}

		if strings.HasSuffix(name, "_test.go") || allowed[name] {
			continue
		}

		content, readErr := os.ReadFile(filepath.Clean(filepath.Join(dir, name)))
		require.NoError(t, readErr)

		for _, loc := range createUserCallRe.FindAllIndex(content, -1) {
			line := 1 + strings.Count(string(content[:loc[0]]), "\n")
			offenders = append(offenders, name+":"+strconv.Itoa(line))
		}
	}

	return offenders
}
