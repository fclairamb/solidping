package models_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestIncidentFailureReason(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		incident *models.Incident
		want     string
	}{
		{"nil incident", nil, ""},
		{"no details", &models.Incident{}, ""},
		{"failure reason", &models.Incident{Details: models.JSONMap{"failure_reason": "HTTP 503"}}, "HTTP 503"},
		{"output fallback", &models.Incident{Details: models.JSONMap{"output": "timeout"}}, "timeout"},
		{
			"failure reason wins over output",
			&models.Incident{Details: models.JSONMap{"failure_reason": "HTTP 503", "output": "timeout"}},
			"HTTP 503",
		},
		{
			"empty failure reason falls back to output",
			&models.Incident{Details: models.JSONMap{"failure_reason": "", "output": "timeout"}},
			"timeout",
		},
		{"non-string value", &models.Incident{Details: models.JSONMap{"failure_reason": 503}}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.incident.FailureReason())
		})
	}
}

func TestCheckTargetURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		check *models.Check
		want  string
	}{
		{"nil check", nil, ""},
		{"no config", &models.Check{}, ""},
		{
			"http check",
			&models.Check{Config: models.JSONMap{"url": "https://api.acme.com/health"}},
			"https://api.acme.com/health",
		},
		{"no url key", &models.Check{Config: models.JSONMap{"host": "acme.com"}}, ""},
		{"non-string url", &models.Check{Config: models.JSONMap{"url": 42}}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.check.TargetURL())
		})
	}
}
