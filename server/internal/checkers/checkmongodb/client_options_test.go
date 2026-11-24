package checkmongodb_test

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkmongodb"
)

// injection carries every URI delimiter and the URI options that matter
// (GHSA-38m7-jhhr-79xx).
const injection = "x@evil.acme.com:27017/?authMechanism=MONGODB-OIDC" +
	"&authMechanismProperties=ENVIRONMENT:k8s&appname=leak&tlsCAFile=/etc/passwd&"

// TestClientOptionsIgnoreURISyntax: a username or a password carrying URI
// syntax stays a plain value; a host carrying it is refused by Validate.
func TestClientOptionsIgnoreURISyntax(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cfg       checkmongodb.MongoDBConfig
		wantHosts []string
		hostError bool
	}{
		{
			name:      "username",
			cfg:       checkmongodb.MongoDBConfig{Host: "db.acme.com", Username: injection, Password: "secret"},
			wantHosts: []string{"db.acme.com:27017"},
		},
		{
			name:      "password",
			cfg:       checkmongodb.MongoDBConfig{Host: "db.acme.com", Username: "monitor", Password: injection},
			wantHosts: []string{"db.acme.com:27017"},
		},
		{
			name:      "username and password",
			cfg:       checkmongodb.MongoDBConfig{Host: "db.acme.com", Username: injection, Password: injection},
			wantHosts: []string{"db.acme.com:27017"},
		},
		{
			name:      "host",
			cfg:       checkmongodb.MongoDBConfig{Host: injection, Username: "monitor", Password: "secret"},
			wantHosts: []string{net.JoinHostPort(injection, "27017")},
			hostError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			opts := checkmongodb.ClientOptions(&tt.cfg, time.Second)

			r.Equal(tt.wantHosts, opts.Hosts)
			r.Nil(opts.AppName)
			r.Nil(opts.TLSConfig)
			r.NotNil(opts.Auth)
			r.Empty(opts.Auth.AuthMechanism)
			r.Empty(opts.Auth.AuthMechanismProperties)
			r.Equal("admin", opts.Auth.AuthSource)
			r.Equal(tt.cfg.Username, opts.Auth.Username)
			r.Equal(tt.cfg.Password, opts.Auth.Password)

			err := tt.cfg.Validate()
			if !tt.hostError {
				r.NoError(err)
				r.NoError(opts.Validate())

				return
			}

			var cfgErr *checkerdef.ConfigError
			r.ErrorAs(err, &cfgErr)
			r.Equal("host", cfgErr.Parameter)
		})
	}
}

// TestClientOptionsPositiveControl: plain credentials reach the driver as is.
func TestClientOptionsPositiveControl(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := checkmongodb.MongoDBConfig{Host: "db.acme.com", Username: "monitor", Password: "p@ss:w/rd?"}
	r.NoError(cfg.Validate())

	opts := checkmongodb.ClientOptions(&cfg, time.Second)
	r.Equal([]string{"db.acme.com:27017"}, opts.Hosts)
	r.Equal("monitor", opts.Auth.Username)
	r.Equal("p@ss:w/rd?", opts.Auth.Password)
	r.True(opts.Auth.PasswordSet)
	r.NoError(opts.Validate())
}

// TestClientOptionsWithoutCredentials: no username, no auth.
func TestClientOptionsWithoutCredentials(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	opts := checkmongodb.ClientOptions(&checkmongodb.MongoDBConfig{Host: "::1", Port: 27018}, time.Second)
	r.Equal([]string{"[::1]:27018"}, opts.Hosts)
	r.Nil(opts.Auth)

	opts = checkmongodb.ClientOptions(&checkmongodb.MongoDBConfig{Socket: "/tmp/mongodb-27017.sock"}, time.Second)
	r.Equal([]string{"localhost"}, opts.Hosts)
}

func TestValidateHost(t *testing.T) {
	t.Parallel()

	refused := []string{"db.acme.com/x", "db.acme.com?x", "user@db.acme.com", "db%2Fx.sock", "a,b", "a#b", "db acme"}
	for _, host := range refused {
		t.Run("refuses "+host, func(t *testing.T) {
			t.Parallel()

			err := (&checkmongodb.MongoDBConfig{Host: host}).Validate()

			var cfgErr *checkerdef.ConfigError
			require.ErrorAs(t, err, &cfgErr)
			require.Equal(t, "host", cfgErr.Parameter)
		})
	}

	for _, host := range []string{"db.acme.com", "10.0.0.5", "::1"} {
		t.Run("accepts "+host, func(t *testing.T) {
			t.Parallel()

			require.NoError(t, (&checkmongodb.MongoDBConfig{Host: host}).Validate())
		})
	}
}
