package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkmysql/config"
)

// The connection is built from structured fields (spec 2026-10-16-04), so
// these characters are harmless at execution; Validate still refuses them in
// the fields a DSN would have carried, as defense in depth.
func TestValidateRejectsDSNMetaCharacters(t *testing.T) {
	t.Parallel()

	base := func() config.MySQLConfig {
		return config.MySQLConfig{Host: "db.acme.com", Username: "monitor", Password: "s3cret", Database: "app"}
	}

	cases := []struct {
		name    string
		mutate  func(c *config.MySQLConfig)
		wantErr string // field name, empty for valid
	}{
		{name: "ordinary config is valid", mutate: func(*config.MySQLConfig) {}},
		{name: "env reference in password is valid", mutate: func(c *config.MySQLConfig) {
			c.Password = "${env:MYSQL_PASSWORD}"
		}},
		{name: "param reference in username is valid", mutate: func(c *config.MySQLConfig) {
			c.Username = "${param:mysql_user}"
		}},
		{name: "driver param injection in database", wantErr: "database", mutate: func(c *config.MySQLConfig) {
			c.Database = "x?allowAllFiles=true&z="
		}},
		{name: "address injection in password", wantErr: "password", mutate: func(c *config.MySQLConfig) {
			c.Password = "X@tcp(10.0.0.5:3306)/e"
		}},
		{name: "at sign in username", wantErr: "username", mutate: func(c *config.MySQLConfig) {
			c.Username = "u@x"
		}},
		{name: "question mark in password", wantErr: "password", mutate: func(c *config.MySQLConfig) {
			c.Password = "p?x"
		}},
		{name: "slash in database", wantErr: "database", mutate: func(c *config.MySQLConfig) {
			c.Database = "a/b"
		}},
		{name: "colon in username", wantErr: "username", mutate: func(c *config.MySQLConfig) {
			c.Username = "u:p"
		}},
		{name: "meta character next to a reference", wantErr: "password", mutate: func(c *config.MySQLConfig) {
			c.Password = "${env:MYSQL_PASSWORD}@tcp(10.0.0.5:3306)/"
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := base()
			tc.mutate(&cfg)

			err := cfg.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)

				return
			}

			var cfgErr *checkerdef.ConfigError
			require.ErrorAs(t, err, &cfgErr)
			require.Equal(t, tc.wantErr, cfgErr.Parameter)
		})
	}
}
