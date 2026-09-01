package registry_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

// credentialNamePatterns are lowercased substrings that unambiguously mark a
// config field as carrying a secret. They are intentionally specific (no bare
// "auth"/"user", which produce false positives like "authProtocol",
// "username") so the tripwire fires only on genuine credentials.
//
// dsn/connectionstring/connection_string cover the "connection strings" category
// from the spec's item 5 (e.g. a future SQL-ish checker embedding
// user:pass@host in a single DSN field).
//
//nolint:gochecknoglobals // test lookup table
var credentialNamePatterns = []string{
	"password",
	"passwd",
	"passphrase",
	"secret",
	"token",
	"apikey",
	"api_key",
	"private_key",
	"privatekey",
	"credential",
	"accesskey",
	"access_key",
	"basicauth",
	"bearer",
	"dsn",
	"connectionstring",
	"connection_string",
}

// credentialNameSuffixes are suffixes that mark a field as a secret when the
// field NAME ends with them (underscore- and case-insensitive) — e.g.
// clientKey, signingKey, ssh_key, private_key, api_key. A SUFFIX match (rather
// than a bare "key" substring anywhere in the name) is deliberate: it catches
// every "...Key" credential shape while leaving "keyword"/"Keyword" alone,
// since "keyword" does not END in "key" — no separate exclusion list needed for
// that false positive.
//
//nolint:gochecknoglobals // test lookup table
var credentialNameSuffixes = []string{
	"key",
}

func looksLikeSecret(jsonName string) bool {
	lower := strings.ToLower(jsonName)
	for _, p := range credentialNamePatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}

	normalized := strings.ReplaceAll(lower, "_", "")
	for _, suffix := range credentialNameSuffixes {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}

	return false
}

// publicByDesignFields are credential-named fields that are DELIBERATELY not
// declared secret because the feature requires them in the public, queryable
// `config` column. The heartbeat/email `token` is a server-generated shared
// secret embedded in a public ping URL / inbound address: the ping handler and
// GetCheckByEmailToken both read it from `config` (public), and the dashboard
// renders the URL/address from it — splitting it out would break the feature.
// See each checker's SecretFields() for the rationale.
//
//nolint:gochecknoglobals // test allowlist
var publicByDesignFields = map[checkerdef.CheckType]map[string]struct{}{
	checkerdef.CheckTypeHeartbeat: {"token": {}},
	checkerdef.CheckTypeEmail:     {"token": {}},
}

func isPublicByDesign(checkType checkerdef.CheckType, jsonName string) bool {
	fields, ok := publicByDesignFields[checkType]
	if !ok {
		return false
	}

	_, ok = fields[jsonName]

	return ok
}

// TestNoUndeclaredCheckerSecrets is the item-5 tripwire: it reflects over every
// registered checker's config struct and asserts that any top-level field whose
// json name looks like a credential — including the password/token/secret/
// basicAuth/bearer family, the "...Key" suffix (clientKey, signingKey, ssh_key,
// private_key, api_key, …), and dsn/connection-string shapes — is declared in
// that config's SecretFields(). A future checker that adds such a field without
// declaring it secret fails here instead of silently re-introducing the leak
// this spec fixed.
//
// Audit finding at the time of writing (re-confirmed after widening the
// heuristic to the key-suffix and dsn/connection-string patterns): every
// genuine credential field across all ~40 checkers is already declared
// (password, token, secretHeaders, basicAuth, private_key, saslPassword,
// authPassword/privPassword, and checkgrpc's secretMetadata — the gRPC request
// metadata added by spec 2026-09-01-03, which is why checkgrpc now declares
// SecretFields() where it previously had none; it still exposes no TLS client
// key). checkdocker/checkbrowser carry no credentials, and checkkubernetes
// declares none because its cluster credentials live on the integration
// connection, not the check config. The widened heuristic's only
// "key"-containing hit across every checker's fields is `keyword`
// (checkgrpc/checkbrowser), which does not END in "key" and so isn't flagged —
// no new publicByDesignFields entry was needed.
func TestNoUndeclaredCheckerSecrets(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for _, checkType := range checkerdef.ListCheckTypes(nil) {
		cfg, ok := registry.ParseConfig(checkType)
		r.Truef(ok, "ParseConfig must know registered type %q", checkType)

		declared := map[string]struct{}{}
		for _, k := range credentials.SecretFieldsFor(cfg) {
			declared[k] = struct{}{}
		}

		for _, jsonName := range topLevelJSONFields(cfg) {
			if !looksLikeSecret(jsonName) {
				continue
			}

			if isPublicByDesign(checkType, jsonName) {
				// Deliberately public (see publicByDesignFields) — must NOT be
				// declared secret, or it would be split out of the queryable
				// public config the feature depends on.
				_, wronglyDeclared := declared[jsonName]
				r.Falsef(wronglyDeclared,
					"checker %q field %q is public-by-design and must NOT be in SecretFields()",
					checkType, jsonName)

				continue
			}

			_, isDeclared := declared[jsonName]
			r.Truef(isDeclared,
				"checker %q config field %q looks like a secret but is NOT declared in SecretFields(); "+
					"add it so it is split out of the public config (see spec 2026-07-18-06)",
				checkType, jsonName)
		}
	}
}

// fakeLeakyConfig is a synthetic config used only to prove the tripwire above
// is not vacuously green: it declares a token field as secret but leaves
// api_key, ClientKey, and Dsn undeclared — the last two exercise the widened
// "key"-suffix and dsn/connection-string patterns specifically, so a future
// checker adding a clientKey/signingKey/sshKey/dsn field without declaring it
// secret is caught, not just the original password/token/basicAuth shapes.
type fakeLeakyConfig struct {
	Host       string `json:"host"`
	Token      string `json:"token"`
	APIKey     string `json:"api_key"` //nolint:tagliatelle // snake_case exercises the api_key pattern
	ClientKey  string `json:"clientKey"`
	Dsn        string `json:"dsn"`
	Passphrase string // no json tag → falls back to field name
	Keyword    string `json:"keyword"`
	ignored    string //nolint:unused // exercises the unexported-skip path
}

func (c *fakeLeakyConfig) SecretFields() []string { return []string{"token"} }

// TestTripwireMechanicsCatchUndeclaredSecret is the positive control for the
// tripwire itself: the detection would flag api_key, clientKey, dsn, and
// Passphrase (undeclared) while accepting token (declared) and ignoring
// host/keyword — so a real regression, including the widened key-suffix and
// dsn patterns, cannot slip past TestNoUndeclaredCheckerSecrets unnoticed.
func TestTripwireMechanicsCatchUndeclaredSecret(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &fakeLeakyConfig{}
	declared := map[string]struct{}{}
	for _, k := range credentials.SecretFieldsFor(cfg) {
		declared[k] = struct{}{}
	}

	var undeclared []string
	for _, jsonName := range topLevelJSONFields(cfg) {
		if !looksLikeSecret(jsonName) {
			continue
		}
		if _, ok := declared[jsonName]; !ok {
			undeclared = append(undeclared, jsonName)
		}
	}

	r.ElementsMatch([]string{"api_key", "clientKey", "dsn", "Passphrase"}, undeclared,
		"the tripwire must flag undeclared credential fields and only those")
	r.False(looksLikeSecret("keyword"), "keyword must not be a false positive (does not END in key)")
	r.False(looksLikeSecret("username"), "username must not be a false positive")
	r.True(looksLikeSecret("basicAuth"))
	r.True(looksLikeSecret("clientKey"), "widened key-suffix pattern must catch clientKey")
	r.True(looksLikeSecret("signingKey"), "widened key-suffix pattern must catch signingKey")
	r.True(looksLikeSecret("ssh_key"), "widened key-suffix pattern must catch ssh_key (underscore-insensitive)")
	r.True(looksLikeSecret("connectionString"), "connection-string pattern must be caught")
	r.True(looksLikeSecret("connection_string"), "connection_string pattern must be caught")
	r.True(looksLikeSecret("dsn"))
}

// topLevelJSONFields returns the serialized (json) names of a config struct's
// top-level exported fields. Fields tagged json:"-" (never serialized) are
// skipped; a field with no json tag falls back to its Go field name, so an
// undeclared `Password string` with no tag is still caught.
func topLevelJSONFields(cfg any) []string {
	v := reflect.ValueOf(cfg)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			v = reflect.New(v.Type().Elem())
		}

		v = v.Elem()
	}

	if v.Kind() != reflect.Struct {
		return nil
	}

	t := v.Type()
	out := make([]string, 0, t.NumField())

	for i := range t.NumField() {
		field := t.Field(i)
		if field.PkgPath != "" {
			// Unexported: never serialized.
			continue
		}

		tag := field.Tag.Get("json")
		name := strings.Split(tag, ",")[0]

		switch name {
		case "-":
			continue
		case "":
			name = field.Name
		}

		out = append(out, name)
	}

	return out
}
