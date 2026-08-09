package importers_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks/importers"
)

func TestUptimeKumaConverterGolden(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	conv := &importers.UptimeKumaConverter{}
	r.Equal("uptime-kuma", conv.Source())

	result, err := conv.Convert(readFixture(t, "uptime-kuma-backup.json"))
	r.NoError(err)

	assertGolden(t, "uptime-kuma", result)
}

func TestUptimeKumaConverterMapsEveryType(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result, err := (&importers.UptimeKumaConverter{}).Convert(readFixture(t, "uptime-kuma-backup.json"))
	r.NoError(err)

	doc := result.Document
	r.Equal("uptime-kuma", doc.Organization)

	site := checkBySlug(t, doc, "website")
	r.Equal("http", site.Type)
	r.Equal("Production", site.Group, "kuma group monitors become check groups")
	r.Equal("Public marketing site", site.Description)
	r.Equal("1m", site.Period)
	r.Equal([]any{"2XX"}, site.Config["expected_status_codes"])
	r.Equal(map[string]any{"X-Tenant": "acme"}, site.Config["headers"])
	// Kuma's default 48s timeout is above SolidPing's 30s cap and is clamped
	// (with a warning) rather than failing the check on import.
	r.Equal("30s", site.Config["timeout"])
	// maxretries (2) × retryInterval (30s) → confirmation period.
	r.NotNil(site.ConfirmationPeriodSeconds)
	r.Equal(60, *site.ConfirmationPeriodSeconds)

	keyword := checkBySlug(t, doc, "docs-keyword")
	r.Equal("http", keyword.Type)
	r.Equal("Documentation", keyword.Config["body_expect"])
	r.Equal([]any{"2XX", "418"}, keyword.Config["expected_status_codes"])

	jsonQuery := checkBySlug(t, doc, "api-json-query")
	r.NotNil(jsonQuery.Config["json_path_assertions"])

	port := checkBySlug(t, doc, "smtp-port")
	r.Equal("tcp", port.Type)
	r.Equal("smtp.example.org", port.Config["host"])
	r.Equal(587, port.Config["port"])

	ping := checkBySlug(t, doc, "gateway")
	r.Equal("icmp", ping.Type)
	r.False(ping.Enabled)

	dns := checkBySlug(t, doc, "dns-record")
	r.Equal("dns", dns.Type)
	r.Equal("1.1.1.1:53", dns.Config["nameserver"], "the dns checker requires host:port")
	r.Equal("A", dns.Config["record_type"])

	docker := checkBySlug(t, doc, "nginx-container")
	r.Equal("docker", docker.Type)
	r.Equal("nginx", docker.Config["containerName"])

	push := checkBySlug(t, doc, "nightly-backup")
	r.Equal("heartbeat", push.Type)

	grpc := checkBySlug(t, doc, "grpc-service")
	r.Equal("grpc", grpc.Type)
	r.Equal("grpc.example.org", grpc.Config["host"])
	r.Equal(50051, grpc.Config["port"])
	r.Equal("health.v1.Health", grpc.Config["serviceName"])
	r.Equal("SERVING", grpc.Config["keyword"])
	r.Equal(true, grpc.Config["tls"])

	mqtt := checkBySlug(t, doc, "broker")
	r.Equal("mqtt", mqtt.Type)
	r.Equal("solidping/health", mqtt.Config["topic"])
	r.Equal("probe", mqtt.Config["username"])
	r.NotContains(mqtt.Config, "password")

	pg := checkBySlug(t, doc, "primary-postgres")
	r.Equal("postgresql", pg.Type)
	r.Equal("pg.example.org", pg.Config["host"])
	r.Equal(5432, pg.Config["port"])
	r.Equal("monitor", pg.Config["username"])
	r.Equal("appdb", pg.Config["database"])
	r.Equal("SELECT 1", pg.Config["query"])
	r.NotContains(pg.Config, "password")

	mysql := checkBySlug(t, doc, "reporting-mysql")
	r.Equal("mysql", mysql.Type)
	r.Equal("reporting", mysql.Config["database"])

	redis := checkBySlug(t, doc, "session-redis")
	r.Equal("redis", redis.Type)
	r.Equal(2, redis.Config["database"])

	mssql := checkBySlug(t, doc, "billing-mssql")
	r.Equal("mssql", mssql.Type)
	r.Equal("mssql.example.org", mssql.Config["host"])
	r.Equal(1433, mssql.Config["port"])
	r.Equal("billing", mssql.Config["database"])
	r.Equal("probe", mssql.Config["username"])
	r.NotContains(mssql.Config, "password")

	mongo := checkBySlug(t, doc, "events-mongo")
	r.Equal("mongodb", mongo.Type)
	r.Equal("mongo.example.org", mongo.Config["host"])

	steam := checkBySlug(t, doc, "game-server")
	r.Equal("a2s", steam.Type)
	r.Equal(27015, steam.Config["port"])

	browser := checkBySlug(t, doc, "checkout-flow")
	r.Equal("browser", browser.Type)
}

func TestUptimeKumaConverterWarnsAndSkips(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result, err := (&importers.UptimeKumaConverter{}).Convert(readFixture(t, "uptime-kuma-backup.json"))
	r.NoError(err)

	r.True(warningMentions(result.Warnings, "notification bindings are not imported"))
	r.True(warningMentions(result.Warnings, `type "radius"`), "unsupported types must warn")
	r.True(warningMentions(result.Warnings, "connection string is empty"))
	r.True(warningMentions(result.Warnings, "700-799"), "unmappable status ranges must warn")
	r.True(warningMentions(result.Warnings, "upside-down"))
	r.True(warningMentions(result.Warnings, "authentication credentials were deliberately not imported"))
	r.True(warningMentions(result.Warnings, "MQTT password"))
	r.True(warningMentions(result.Warnings, "database password"))
	r.True(warningMentions(result.Warnings, "tags are not imported"))
	r.True(warningMentions(result.Warnings, "ping URL changes"))

	// ignoreTls maps to verifySsl: false on http-type monitors instead of warning.
	authAPI := checkBySlug(t, result.Document, "authenticated-api")
	r.Equal(false, authAPI.Config["verifySsl"], "ignoreTls: true maps to verifySsl: false")

	// Group monitors never become checks, unsupported/invalid monitors are dropped.
	for i := range result.Document.Checks {
		slug := result.Document.Checks[i].Slug
		r.NotEqual("production", slug)
		r.NotEqual("radius-login", slug)
		r.NotEqual("broken-db", slug)
	}
}

func TestUptimeKumaConverterRejectsBadInput(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	conv := &importers.UptimeKumaConverter{}

	_, err := conv.Convert([]byte("not json"))
	r.Error(err)

	_, err = conv.Convert([]byte(`{"version":"2.0.0","monitorList":[]}`))
	r.ErrorIs(err, importers.ErrEmptyInput)
	r.Contains(err.Error(), "2.x")
}
