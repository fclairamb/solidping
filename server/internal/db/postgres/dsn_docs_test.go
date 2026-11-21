package postgres

import (
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// socketDSNRE matches a postgres URL whose host is empty (`@/`), the form the
// docs give for a Unix socket.
var socketDSNRE = regexp.MustCompile(`postgres(?:ql)?://[^\s"'@]*@/[^\s"']*`)

// TestDocsUnixSocketDSNExamples keeps the documented Unix-socket DSNs working:
// every page must show one, and each must reach the drivers as a socket path.
func TestDocsUnixSocketDSNExamples(t *testing.T) {
	t.Parallel()

	// The pages that show a Unix-socket SP_DB_URL: the Docker install page and
	// the database configuration reference.
	pages := []string{
		"../../../../web/docs/docs/installation/docker.md",
		"../../../../web/docs/docs/configuration/database.md",
	}

	for _, page := range pages {
		t.Run(path.Base(page), func(t *testing.T) {
			t.Parallel()

			raw, err := os.ReadFile(page)
			require.NoError(t, err)

			examples := socketDSNRE.FindAllString(string(raw), -1)
			require.NotEmpty(t, examples, "%s must show a Unix-socket SP_DB_URL", page)

			for _, dsn := range examples {
				_, _, _, ok := parseSocketDSN(dsn)
				require.True(t, ok, "documented DSN %q is not read as a socket DSN", dsn)

				driver, err := url.Parse(DriverDSN(dsn))
				require.NoError(t, err)
				require.True(t, strings.HasPrefix(path.Base(driver.Query().Get("host")), socketFilePrefix),
					"documented DSN %q does not reach pgdriver as a socket file", dsn)
			}

			require.Contains(t, string(raw), "port=5433", "%s must explain the non-default port", page)
		})
	}
}

// TestPercentEncodedSocketHostRejected backs the docs' warning that
// `@%2Fvar%2Frun%2Fpostgresql/` does not work: Go's URL parser refuses it.
func TestPercentEncodedSocketHostRejected(t *testing.T) {
	t.Parallel()

	encoded := "postgresql://user:password@" + url.PathEscape("/var/run/postgresql") + "/solidping"
	require.Contains(t, encoded, "%2Fvar%2Frun%2Fpostgresql")

	_, err := url.Parse(encoded)
	require.Error(t, err)
}
