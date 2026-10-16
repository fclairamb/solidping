package systemconfig

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// authDocsExempt lists auth.* parameters whose environment variable is
// deliberately not documented in authentication.md, with the reason. Keep it
// empty unless a variable is internal and nobody should set it.
func authDocsExempt() map[string]string {
	return map[string]string{}
}

// authDocsExemptPrefixes lists parameter-key prefixes whose documentation is
// tracked by its own spec, so they cannot be required yet. Remove an entry
// when its section lands in authentication.md.
func authDocsExemptPrefixes() map[string]string {
	return map[string]string{
		"auth.oidc.": "generic OIDC section: separate spec",
		"auth.saml.": "SAML section: separate spec",
	}
}

// TestAuthEnvVarsDocumented keeps web/docs/docs/configuration/authentication.md
// in step with the auth.* system parameters: every one with an environment
// variable must be named in the page, so a new setting cannot ship undocumented.
func TestAuthEnvVarsDocumented(t *testing.T) {
	t.Parallel()

	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)

	// server/internal/systemconfig -> repo root.
	root := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	path := filepath.Join(root, "web", "docs", "docs", "configuration", "authentication.md")

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	doc := string(raw)
	allDocs := readAllConfigDocs(t, filepath.Dir(path))
	params := getKnownParameters()

	checked := 0
	ldap := 0
	missing := []string{}

	for i := range params {
		key := string(params[i].Key)
		if !strings.HasPrefix(key, "auth.") || params[i].EnvVar == "" {
			continue
		}

		if _, exempt := authDocsExempt()[key]; exempt {
			continue
		}

		if hasExemptPrefix(key) {
			continue
		}

		checked++

		if strings.HasPrefix(key, "auth.ldap.") {
			ldap++
		}

		// LDAP must be in authentication.md itself; the other providers may
		// be documented on a sibling page (Slack and Discord bots live in
		// notifications.md).
		haystack := allDocs
		if strings.HasPrefix(key, "auth.ldap.") {
			haystack = doc
		}

		if !containsWord(haystack, params[i].EnvVar) {
			missing = append(missing, params[i].EnvVar+" ("+key+")")
		}
	}

	require.Empty(t, strings.Join(missing, ", "), "auth.* variables not documented in authentication.md")

	require.Equal(t, 10, ldap, "the ten SP_LDAP_* variables must all be checked")
	require.Positive(t, checked)
}

// containsWord reports whether name appears in doc as a whole identifier, so
// SP_LDAP_ENABLED is not satisfied by SP_LDAP_ENABLED_FOO.
func containsWord(doc, name string) bool {
	for from := 0; ; {
		idx := strings.Index(doc[from:], name)
		if idx < 0 {
			return false
		}

		end := from + idx + len(name)
		if end >= len(doc) || !isIdentChar(doc[end]) {
			return true
		}

		from = end
	}
}

func isIdentChar(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z')
}

func TestContainsWord(t *testing.T) {
	t.Parallel()

	require.True(t, containsWord("set `SP_LDAP_ENABLED` now", "SP_LDAP_ENABLED"))
	require.True(t, containsWord("SP_LDAP_ENABLED", "SP_LDAP_ENABLED"))
	require.False(t, containsWord("SP_LDAP_ENABLED_FOO", "SP_LDAP_ENABLED"))
	require.False(t, containsWord("nothing", "SP_LDAP_ENABLED"))
}

func hasExemptPrefix(key string) bool {
	for prefix := range authDocsExemptPrefixes() {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}

	return false
}

// readAllConfigDocs concatenates every page of the configuration docs.
func readAllConfigDocs(t *testing.T, dir string) string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	var sb strings.Builder

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}

		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)

		sb.Write(b)
		sb.WriteByte('\n')
	}

	return sb.String()
}
