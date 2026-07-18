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
// "key"/"auth"/"user", which produce false positives like "keyword",
// "authProtocol", "username") so the tripwire fires only on genuine credentials.
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
}

func looksLikeSecret(jsonName string) bool {
	lower := strings.ToLower(jsonName)
	for _, p := range credentialNamePatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}

	return false
}

// TestNoUndeclaredCheckerSecrets is the item-5 tripwire: it reflects over every
// registered checker's config struct and asserts that any top-level field whose
// json name looks like a credential is declared in that config's SecretFields().
// A future checker that adds a `password`/`token`/`secretXxx` field without
// declaring it secret fails here instead of silently re-introducing the leak
// this spec fixed.
//
// Audit finding at the time of writing: every genuine credential field across
// all ~40 checkers is already declared (password, token, secretHeaders,
// basicAuth, private_key, saslPassword, authPassword/privPassword). checkgrpc
// exposes no TLS client key, checkdocker/checkbrowser carry no credentials, and
// checkkubernetes declares none because its cluster credentials live on the
// integration connection, not the check config.
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

			_, isDeclared := declared[jsonName]
			r.Truef(isDeclared,
				"checker %q config field %q looks like a secret but is NOT declared in SecretFields(); "+
					"add it so it is split out of the public config (see spec 2026-07-18-06)",
				checkType, jsonName)
		}
	}
}

// fakeLeakyConfig is a synthetic config used only to prove the tripwire above
// is not vacuously green: it declares a token field as secret but leaves an
// api_key field undeclared.
type fakeLeakyConfig struct {
	Host       string `json:"host"`
	Token      string `json:"token"`
	APIKey     string `json:"api_key"`
	Passphrase string // no json tag → falls back to field name
	Keyword    string `json:"keyword"`
	ignored    string //nolint:unused // exercises the unexported-skip path
}

func (c *fakeLeakyConfig) SecretFields() []string { return []string{"token"} }

// TestTripwireMechanicsCatchUndeclaredSecret is the positive control for the
// tripwire itself: the detection would flag api_key and Passphrase (undeclared)
// while accepting token (declared) and ignoring host/keyword — so a real
// regression cannot slip past TestNoUndeclaredCheckerSecrets unnoticed.
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

	r.ElementsMatch([]string{"api_key", "Passphrase"}, undeclared,
		"the tripwire must flag undeclared credential fields and only those")
	r.False(looksLikeSecret("keyword"), "keyword must not be a false positive")
	r.False(looksLikeSecret("username"), "username must not be a false positive")
	r.True(looksLikeSecret("basicAuth"))
}

// topLevelJSONFields returns the serialized (json) names of a config struct's
// top-level exported fields. Fields tagged json:"-" (never serialized) are
// skipped; a field with no json tag falls back to its Go field name, so an
// undeclared `Password string` with no tag is still caught.
func topLevelJSONFields(cfg any) []string {
	v := reflect.ValueOf(cfg)
	for v.Kind() == reflect.Ptr {
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
