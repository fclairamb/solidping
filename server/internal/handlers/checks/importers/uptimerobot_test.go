package importers_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks/importers"
)

func TestUptimeRobotConverterGolden(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	conv := &importers.UptimeRobotConverter{}
	r.Equal("uptimerobot", conv.Source())

	result, err := conv.Convert(readFixture(t, "uptimerobot-monitors.json"))
	r.NoError(err)

	assertGolden(t, "uptimerobot", result)
}

func TestUptimeRobotConverterMapsEveryType(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result, err := (&importers.UptimeRobotConverter{}).Convert(readFixture(t, "uptimerobot-monitors.json"))
	r.NoError(err)

	doc := result.Document
	r.Equal("uptimerobot", doc.Organization)

	site := checkBySlug(t, doc, "main-website")
	r.Equal("http", site.Type)
	r.Equal("https://example.org", site.Config["url"])
	r.Equal("15s", site.Config["timeout"])
	r.True(site.Enabled)

	keywordExists := checkBySlug(t, doc, "docs-keyword-check")
	r.Equal("http", keywordExists.Type)
	r.Equal("Documentation", keywordExists.Config["body_expect"])
	r.NotContains(keywordExists.Config, "body_reject")

	keywordNotExists := checkBySlug(t, doc, "broken-widget")
	r.Equal("http", keywordNotExists.Type)
	r.Equal("Error", keywordNotExists.Config["body_reject"])
	r.NotContains(keywordNotExists.Config, "body_expect")

	ping := checkBySlug(t, doc, "core-gateway")
	r.Equal("icmp", ping.Type)
	r.Equal("198.51.100.10", ping.Config["host"])

	port := checkBySlug(t, doc, "smtp-relay")
	r.Equal("tcp", port.Type)
	r.Equal("smtp.example.org", port.Config["host"])
	r.Equal(25, port.Config["port"], "sub_type 4 (SMTP) resolves to the well-known port")

	customPort := checkBySlug(t, doc, "custom-app-port")
	r.Equal("tcp", customPort.Type)
	r.Equal(8443, customPort.Config["port"], "sub_type 99 reads the port field")

	heartbeat := checkBySlug(t, doc, "nightly-backup-ping")
	r.Equal("heartbeat", heartbeat.Type)

	paused := checkBySlug(t, doc, "paused-legacy-site")
	r.False(paused.Enabled, "status: 0 maps to a disabled check")

	basicAuth := checkBySlug(t, doc, "admin-portal")
	r.Equal("opuser:s3cret", basicAuth.Config["basicAuth"])

	digestAuth := checkBySlug(t, doc, "legacy-digest-auth")
	r.NotContains(digestAuth.Config, "basicAuth", "only basic auth is imported, not digest")
}

func TestUptimeRobotConverterWarnsAndSkips(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result, err := (&importers.UptimeRobotConverter{}).Convert(readFixture(t, "uptimerobot-monitors.json"))
	r.NoError(err)

	r.True(warningMentions(result.Warnings, "alert contacts and maintenance windows are not imported"))
	r.True(warningMentions(result.Warnings, "type 6"), "unsupported types must warn")
	r.True(warningMentions(result.Warnings, "ping URL changes"))
	r.True(warningMentions(result.Warnings, "only basic HTTP authentication is imported"))
	r.True(warningMentions(result.Warnings, "custom HTTP status rules are not imported"))
	r.True(warningMentions(result.Warnings, "custom HTTP headers are not imported"))
	r.True(warningMentions(result.Warnings, "public status page"))

	// The unsupported monitor type never becomes a check.
	for i := range result.Document.Checks {
		r.NotEqual("unmapped-watchdog", result.Document.Checks[i].Slug)
	}
}

func TestUptimeRobotConverterRejectsBadInput(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	conv := &importers.UptimeRobotConverter{}

	_, err := conv.Convert([]byte("not json"))
	r.Error(err)

	_, err = conv.Convert([]byte(``))
	r.ErrorIs(err, importers.ErrEmptyInput)

	_, err = conv.Convert([]byte(`{"stat":"ok","monitors":[]}`))
	r.ErrorIs(err, importers.ErrEmptyInput)

	_, err = conv.Convert([]byte(`true`))
	r.Error(err, "neither an object nor an array must be rejected")
}

// TestUptimeRobotConverterAcceptsBareMonitorsArray covers the second input
// shape: a plain `monitors` array with no wrapping response object.
func TestUptimeRobotConverterAcceptsBareMonitorsArray(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result, err := (&importers.UptimeRobotConverter{}).Convert(readFixture(t, "uptimerobot-array.json"))
	r.NoError(err)
	r.Len(result.Document.Checks, 2)

	site := checkBySlug(t, result.Document, "bare-array-site")
	r.Equal("http", site.Type)

	ping := checkBySlug(t, result.Document, "bare-array-ping")
	r.Equal("icmp", ping.Type)
}

// TestUptimeRobotConverterAcceptsPaginatedPages covers the third input shape:
// an array of concatenated paginated getMonitors responses.
func TestUptimeRobotConverterAcceptsPaginatedPages(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result, err := (&importers.UptimeRobotConverter{}).Convert(readFixture(t, "uptimerobot-paginated.json"))
	r.NoError(err)
	r.Len(result.Document.Checks, 3, "monitors from every page must be concatenated")

	checkBySlug(t, result.Document, "page-one-site")
	checkBySlug(t, result.Document, "page-one-ping")
	checkBySlug(t, result.Document, "page-two-site")
}

// TestUptimeRobotConverterPortSubTypeFallback covers the two Port sub_type
// branches not exercised by the golden fixture: an unrecognized sub_type
// (falls back to the `port` field with a warning) and a missing/zero sub_type
// (falls back to the `port` field without a warning, same as sub_type 99).
func TestUptimeRobotConverterPortSubTypeFallback(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	conv := &importers.UptimeRobotConverter{}

	unknownSubType := []byte(`{"monitors":[
	  {"id":1,"friendly_name":"Odd subtype port","url":"odd.example.org","type":4,"sub_type":"7","port":"6000",
	   "interval":300,"status":2}
	]}`)
	result, err := conv.Convert(unknownSubType)
	r.NoError(err)
	check := checkBySlug(t, result.Document, "odd-subtype-port")
	r.Equal(6000, check.Config["port"])
	r.True(warningMentions(result.Warnings, "unknown port sub_type 7"))

	noSubType := []byte(`{"monitors":[
	  {"id":1,"friendly_name":"Fallback port check","url":"fallback.example.org","type":4,"port":"9000",
	   "interval":300,"status":2}
	]}`)
	result, err = conv.Convert(noSubType)
	r.NoError(err)
	check = checkBySlug(t, result.Document, "fallback-port-check")
	r.Equal(9000, check.Config["port"])
	r.False(warningMentions(result.Warnings, "unknown port sub_type"))

	noPort := []byte(`{"monitors":[
	  {"id":1,"friendly_name":"No port at all","url":"noport.example.org","type":4,
	   "interval":300,"status":2}
	]}`)
	result, err = conv.Convert(noPort)
	r.NoError(err)
	check = checkBySlug(t, result.Document, "no-port-at-all")
	r.NotContains(check.Config, "port")
	r.True(warningMentions(result.Warnings, "no resolvable port"))
}

// TestUptimeRobotConverterWellKnownPortSubTypes asserts every entry of the
// sub_type → well-known-port lookup table individually. The golden fixture
// only exercises sub_type 4 (SMTP) and 99 (custom); a transposed pair
// anywhere else in that table would otherwise ship with zero coverage.
func TestUptimeRobotConverterWellKnownPortSubTypes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		subType int
		port    int
		label   string
	}{
		{1, 80, "HTTP"},
		{2, 443, "HTTPS"},
		{3, 21, "FTP"},
		{4, 25, "SMTP"},
		{5, 110, "POP3"},
		{6, 143, "IMAP"},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			body := []byte(fmt.Sprintf(`{"monitors":[
			  {"id":1,"friendly_name":"Well known %s","url":"port.example.org","type":4,"sub_type":"%d",
			   "interval":300,"status":2}
			]}`, tc.label, tc.subType))

			result, err := (&importers.UptimeRobotConverter{}).Convert(body)
			r.NoError(err)

			check := checkBySlug(t, result.Document, "well-known-"+strings.ToLower(tc.label))
			r.Equal(tc.port, check.Config["port"], "sub_type %d must resolve to port %d", tc.subType, tc.port)
			r.False(warningMentions(result.Warnings, "unknown port sub_type"),
				"a well-known sub_type must not warn")
		})
	}
}
