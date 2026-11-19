package postgres

import (
	"net/url"
	"path"
	"strings"
)

// socketFilePrefix is the name PostgreSQL gives its Unix-domain socket inside
// the socket directory: "<dir>/.s.PGSQL.<port>".
const socketFilePrefix = ".s.PGSQL."

// defaultPort is the port PostgreSQL (and the socket file name) defaults to.
const defaultPort = "5432"

// DriverDSN adapts a libpq-style Unix-socket DSN to bun's pgdriver.
//
// libpq (and lib/pq, used by the LISTEN notifier) read `host=/var/run/postgresql`
// as the socket DIRECTORY and connect to "<dir>/.s.PGSQL.<port>". pgdriver
// instead dials the `host` value itself, so the directory form fails with a
// connect error. This rewrites `host` to the socket file, taking the port from
// the `port` query parameter, the URL's port, or 5432, and drops the `port`
// query parameter (pgdriver would otherwise send it to the server as a startup
// parameter, which Postgres rejects). Any other DSN is returned unchanged.
func DriverDSN(dsn string) string {
	parsed, query, socketHost, ok := parseSocketDSN(dsn)
	if !ok {
		return dsn
	}

	if !strings.HasPrefix(path.Base(socketHost), socketFilePrefix) {
		port := query.Get("port")
		if port == "" {
			port = parsed.Port()
		}

		if port == "" {
			port = defaultPort
		}

		socketHost = path.Join(socketHost, socketFilePrefix+port)
	}

	query.Set("host", socketHost)
	query.Del("port")
	parsed.RawQuery = query.Encode()

	return parsed.String()
}

// ListenerDSN adapts a Unix-socket DSN to lib/pq, which the LISTEN/NOTIFY
// notifier uses. It is the inverse of DriverDSN: lib/pq wants the socket
// directory in `host` and appends the socket file name itself, so a `host`
// naming the socket file ("<dir>/.s.PGSQL.<port>") is split back into the
// directory and the `port` query parameter. Any other DSN is returned unchanged.
func ListenerDSN(dsn string) string {
	parsed, query, socketHost, ok := parseSocketDSN(dsn)
	if !ok {
		return dsn
	}

	base := path.Base(socketHost)
	if !strings.HasPrefix(base, socketFilePrefix) {
		return dsn
	}

	query.Set("host", path.Dir(socketHost))
	query.Set("port", strings.TrimPrefix(base, socketFilePrefix))
	parsed.RawQuery = query.Encode()

	return parsed.String()
}

// parseSocketDSN parses a postgres:// URL DSN whose `host` query parameter is
// an absolute path, the libpq way of asking for a Unix-domain socket. ok is
// false for any other DSN (key=value form, unix:// scheme, TCP host).
func parseSocketDSN(dsn string) (*url.URL, url.Values, string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return nil, nil, "", false
	}

	query := parsed.Query()

	host := query.Get("host")
	if !strings.HasPrefix(host, "/") {
		return nil, nil, "", false
	}

	return parsed, query, host, true
}
