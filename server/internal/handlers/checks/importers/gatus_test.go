package importers_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/checks/importers"
)

// checkBySlug finds a converted check by slug.
func checkBySlug(t *testing.T, doc *checks.ExportDocument, slug string) checks.ExportCheck {
	t.Helper()

	for i := range doc.Checks {
		if doc.Checks[i].Slug == slug {
			return doc.Checks[i]
		}
	}

	require.FailNowf(t, "check not found", "no check with slug %q", slug)

	return checks.ExportCheck{}
}

// warningMentions reports whether any warning message contains the substring.
func warningMentions(ws []importers.ConversionWarning, substr string) bool {
	for _, w := range ws {
		if contains(w.Message, substr) || contains(w.Field, substr) || contains(w.Item, substr) {
			return true
		}
	}

	return false
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}

	return -1
}

func TestGatusConverterGolden(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	conv := &importers.GatusConverter{}
	r.Equal("gatus", conv.Source())

	result, err := conv.Convert(readFixture(t, "gatus-config.yaml"))
	r.NoError(err)

	assertGolden(t, "gatus", result)
}

func TestGatusConverterMapsCoreFields(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result, err := (&importers.GatusConverter{}).Convert(readFixture(t, "gatus-config.yaml"))
	r.NoError(err)

	doc := result.Document
	r.Equal(1, doc.Version)
	r.Equal("gatus", doc.Organization)

	backEnd := checkBySlug(t, doc, "back-end")
	r.Equal("http", backEnd.Type)
	r.Equal("core", backEnd.Group)
	r.Equal("30s", backEnd.Period)
	r.True(backEnd.Enabled)
	r.Equal("https://example.org/health", backEnd.Config["url"])
	r.Equal("POST", backEnd.Config["method"])
	r.Equal(200, backEnd.Config["expected_status"])
	r.Equal("5s", backEnd.Config["timeout"])
	r.NotNil(backEnd.Config["json_path_assertions"])

	// Certificate expiration is split into a dedicated ssl check.
	ssl := checkBySlug(t, doc, "back-end-ssl")
	r.Equal("ssl", ssl.Type)
	r.Equal("example.org", ssl.Config["host"])
	r.Equal(2, ssl.Config["criticalDays"])

	// Domain expiration is split into a dedicated domain check.
	domain := checkBySlug(t, doc, "marketing-site-domain")
	r.Equal("domain", domain.Type)
	r.Equal("www.example.org", domain.Config["domain"])
	r.Equal(30, domain.Config["thresholdDays"])

	site := checkBySlug(t, doc, "marketing-site")
	r.Equal([]any{"200", "301"}, site.Config["expected_status_codes"])
	r.Equal("^.*Welcome.*$", site.Config["body_pattern"])
	r.Equal(false, site.Config["verifySsl"], "client.insecure maps to verifySsl: false")
	r.Equal(false, site.Config["followRedirects"], "client.ignore-redirect maps to followRedirects: false")

	legacy := checkBySlug(t, doc, "legacy-soap")
	r.False(legacy.Enabled)
	r.Equal([]any{"2XX"}, legacy.Config["expected_status_codes"])
	r.Equal("nope", legacy.Config["body_reject"])

	db := checkBySlug(t, doc, "database-port")
	r.Equal("tcp", db.Type)
	r.Equal("db.example.org", db.Config["host"])
	r.Equal(5432, db.Config["port"])

	relay := checkBySlug(t, doc, "secure-relay")
	r.Equal("tcp", relay.Type)
	r.Equal(true, relay.Config["tls"])

	ping := checkBySlug(t, doc, "gateway-ping")
	r.Equal("icmp", ping.Type)
	r.Equal("gw.example.org", ping.Config["host"])
	r.Equal("2m", ping.Period)

	bastion := checkBySlug(t, doc, "bastion")
	r.Equal("ssh", bastion.Type)
	r.Equal("bastion.example.org", bastion.Config["host"])
	// Credentials are never imported, and a username with no credential would
	// fail checker validation — so neither half is carried over.
	r.NotContains(bastion.Config, "username")
	r.NotContains(bastion.Config, "password")

	udp := checkBySlug(t, doc, "syslog-relay")
	r.Equal("udp", udp.Type)
	r.Equal(514, udp.Config["port"])

	ws := checkBySlug(t, doc, "realtime-feed")
	r.Equal("websocket", ws.Type)
	r.Equal("wss://feed.example.org/socket", ws.Config["url"])

	dns := checkBySlug(t, doc, "dns-lookup")
	r.Equal("dns", dns.Type)
	r.Equal("example.org", dns.Config["host"])
	r.Equal("A", dns.Config["record_type"])
	r.Equal("8.8.8.8:53", dns.Config["nameserver"], "the dns checker requires host:port")
	r.Equal([]any{"93.184.216.34"}, dns.Config["expected_values"])
}

func TestGatusConverterWarnsOnUnmappableItems(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result, err := (&importers.GatusConverter{}).Convert(readFixture(t, "gatus-config.yaml"))
	r.NoError(err)

	// Unmappable condition families are surfaced, not silently dropped.
	r.True(warningMentions(result.Warnings, "[RESPONSE_TIME]"), "response time condition must warn")
	r.True(warningMentions(result.Warnings, "len()"), "len() condition must warn")
	r.True(warningMentions(result.Warnings, "alerting/notification"), "alerts must warn")
	r.True(warningMentions(result.Warnings, "external-endpoint"), "external endpoints must warn")
	r.True(warningMentions(result.Warnings, "sctp"), "unsupported scheme must warn")
	r.True(warningMentions(result.Warnings, "SSH credentials were deliberately not imported"),
		"ssh credentials must warn")

	// …but the unsupported endpoint is the only one dropped.
	for i := range result.Document.Checks {
		r.NotEqual("unsupported-transport", result.Document.Checks[i].Slug)
	}
}

func TestGatusConverterRejectsBadInput(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	conv := &importers.GatusConverter{}

	_, err := conv.Convert([]byte("::: not yaml :::"))
	r.Error(err)

	_, err = conv.Convert([]byte("storage:\n  type: sqlite\n"))
	r.ErrorIs(err, importers.ErrEmptyInput)
}

func TestGatusConverterDedupesSlugs(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	input := []byte(`
endpoints:
  - name: api
    url: "https://a.example.org"
    conditions: ["[STATUS] == 200"]
  - name: API
    url: "https://b.example.org"
    conditions: ["[STATUS] == 200"]
`)

	result, err := (&importers.GatusConverter{}).Convert(input)
	r.NoError(err)
	r.Len(result.Document.Checks, 2)
	r.Equal("api", result.Document.Checks[0].Slug)
	r.Equal("api-2", result.Document.Checks[1].Slug)
}
