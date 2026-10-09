package notifications

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Drift guard (spec 2026-10-08-03). A Pushover integration created from the
// dashboard never delivered because three layers named the same two settings
// three ways: the form wrote `user`/`token`, the sender read `userKey`/
// `apiToken`, the secret registry encrypted `user_key`/`api_token`. These
// tests read the dashboard form's source and fail when:
//   - a key the form writes for a type is not a key that type's sender reads;
//   - a key the form edits in a secret input is not in the secret registry
//     (crypto/credentials/conn_secrets.go), so it would be stored in clear.

const integrationFormPath = "../../../web/dash0/src/components/integrations/integration-form.tsx"

// senderSettingKeys lists, per connection type, every settings key its sender
// reads. Struct-based senders derive the list from their JSON tags so it can
// not go stale; map-based senders list their reads by hand.
func senderSettingKeys() map[models.ConnectionType][]string {
	return map[models.ConnectionType][]string{
		models.ConnectionTypePushover:     jsonTagNames(pushoverSettings{}),
		models.ConnectionTypeNtfy:         jsonTagNames(ntfySettings{}),
		models.ConnectionTypeGotify:       jsonTagNames(gotifySettings{}),
		models.ConnectionTypeMatrix:       jsonTagNames(matrixSettings{}),
		models.ConnectionTypeMattermost:   jsonTagNames(mattermostSettings{}),
		models.ConnectionTypeGoogleChat:   jsonTagNames(googleChatSettings{}),
		models.ConnectionTypeSlackWebhook: jsonTagNames(slackWebhookSettings{}),
		models.ConnectionTypeZulip:        jsonTagNames(zulipSettings{}),
		models.ConnectionTypePagerduty:    jsonTagNames(pagerdutySettings{}),
		models.ConnectionTypeMSTeams:      jsonTagNames(models.MSTeamsSettings{}),
		models.ConnectionTypeTwilio:       jsonTagNames(models.TwilioSettings{}),
		models.ConnectionTypeEmail:        {"to", "subject_prefix"},
		models.ConnectionTypeWebhook:      {"url", "headers"},
	}
}

// formTypesWithoutSenderCheck are types whose form panel is not a list of
// delivery settings for a notification sender. Every other type the form
// edits must have an entry in senderSettingKeys.
//
//nolint:gochecknoglobals // constant lookup table
var formTypesWithoutSenderCheck = map[models.ConnectionType]string{
	models.ConnectionTypeKubernetes: "data source, read by integrationk8s, not a notification sender",
}

// mustCover are the types whose panels write their settings through
// update("key"). If the parser stops finding one of them, the guard has gone
// blind and must fail rather than pass on nothing.
//
//nolint:gochecknoglobals // constant lookup table
var mustCover = []models.ConnectionType{
	models.ConnectionTypeWebhook, models.ConnectionTypeGoogleChat, models.ConnectionTypeMattermost,
	models.ConnectionTypeSlackWebhook, models.ConnectionTypeMSTeams, models.ConnectionTypeEmail,
	models.ConnectionTypeNtfy, models.ConnectionTypeGotify, models.ConnectionTypeMatrix,
	models.ConnectionTypeZulip, models.ConnectionTypePushover, models.ConnectionTypePagerduty,
	models.ConnectionTypeTwilio, models.ConnectionTypeKubernetes,
}

func jsonTagNames(v any) []string {
	t := reflect.TypeOf(v)
	names := make([]string, 0, t.NumField())

	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			names = append(names, name)
		}
	}

	return names
}

// formPanelKeys is what the dashboard form writes for one connection type.
type formPanelKeys struct {
	all     []string
	secrets []string
}

var (
	reCase        = regexp.MustCompile(`^\s*case "([a-z0-9-]+)":`)
	reUpdateKey   = regexp.MustCompile(`update\("([A-Za-z0-9_]+)"`)
	rePanelRef    = regexp.MustCompile(`<([A-Z][A-Za-z0-9]*Panel)\b`)
	reSecretPanel = regexp.MustCompile(`(?s)<SecretPanel\b.*?/>`)
)

// parseFormKeys extracts, per connection type, the keys PerTypePanel writes
// with update("key"), following the *Panel components a case delegates to.
func parseFormKeys(t *testing.T) map[models.ConnectionType]formPanelKeys {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean(integrationFormPath))
	require.NoError(t, err, "the drift guard reads the dashboard form source")

	src := string(raw)
	body := functionBody(t, src, "PerTypePanel")

	type block struct {
		types []string
		text  strings.Builder
	}

	var blocks []*block

	var cur *block

	for _, line := range strings.Split(body, "\n") {
		if m := reCase.FindStringSubmatch(line); m != nil {
			// Consecutive case labels share one body (fallthrough grouping).
			if cur == nil || strings.TrimSpace(cur.text.String()) != "" {
				cur = &block{}
				blocks = append(blocks, cur)
			}

			cur.types = append(cur.types, m[1])

			continue
		}

		if cur != nil {
			cur.text.WriteString(line)
			cur.text.WriteByte('\n')
		}
	}

	out := map[models.ConnectionType]formPanelKeys{}

	for _, b := range blocks {
		text := expandPanels(src, b.text.String(), map[string]bool{})
		keys := formPanelKeys{all: uniqueMatches(reUpdateKey, text)}

		for _, secretUse := range reSecretPanel.FindAllString(text, -1) {
			keys.secrets = append(keys.secrets, uniqueMatches(reUpdateKey, secretUse)...)
		}

		if len(keys.all) == 0 {
			continue
		}

		for _, typ := range b.types {
			out[models.ConnectionType(typ)] = keys
		}
	}

	return out
}

// expandPanels appends the source of every *Panel component text renders, so
// a case that delegates (e.g. twilio → TwilioPanel) is checked too.
func expandPanels(src, text string, seen map[string]bool) string {
	var sb strings.Builder

	sb.WriteString(text)

	for _, m := range rePanelRef.FindAllStringSubmatch(text, -1) {
		name := m[1]
		if seen[name] || name == "SecretPanel" || name == "UrlPanel" {
			continue
		}

		seen[name] = true

		start := strings.Index(src, "\nfunction "+name+"(")
		if start < 0 {
			continue
		}

		end := strings.Index(src[start+1:], "\nfunction ")
		if end < 0 {
			end = len(src) - start - 1
		}

		sb.WriteString(expandPanels(src, src[start:start+1+end], seen))
	}

	return sb.String()
}

func functionBody(t *testing.T, src, name string) string {
	t.Helper()

	start := strings.Index(src, "\nfunction "+name+"(")
	require.GreaterOrEqual(t, start, 0, "function %s not found in the form", name)

	end := strings.Index(src[start+1:], "\nfunction ")
	require.Positive(t, end)

	return src[start : start+1+end]
}

func uniqueMatches(re *regexp.Regexp, text string) []string {
	var out []string

	for _, m := range re.FindAllStringSubmatch(text, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}

	sort.Strings(out)

	return out
}

func TestSettingsDrift_FormParserCoversEveryEditablePanel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	form := parseFormKeys(t)
	for _, typ := range mustCover {
		r.NotEmpty(form[typ].all, "no keys parsed for %s: the drift guard can no longer see its panel", typ)
	}

	// The panel that caused spec 2026-10-08-03.
	r.Equal([]string{"api_token", "user_key"}, form[models.ConnectionTypePushover].all)
	r.ElementsMatch([]string{"api_token", "user_key"}, form[models.ConnectionTypePushover].secrets)
}

func TestSettingsDrift_FormKeysAreReadBySender(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	senders := senderSettingKeys()

	for typ, keys := range parseFormKeys(t) {
		if _, skip := formTypesWithoutSenderCheck[typ]; skip {
			continue
		}

		read, ok := senders[typ]
		r.True(ok, "the form edits %q settings but senderSettingKeys has no entry for it", typ)

		for _, key := range keys.all {
			r.Contains(read, key, "the form writes %s.%s but the %s sender never reads it", typ, key, typ)
		}
	}
}

func TestSettingsDrift_FormSecretsAreEncrypted(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for typ, keys := range parseFormKeys(t) {
		registered := credentials.ConnectionSecretFields(typ)

		for _, key := range keys.secrets {
			r.Contains(registered, key,
				"the form edits %s.%s in a secret input but conn_secrets.go does not encrypt it", typ, key)
		}
	}
}
