package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyFeedbackEnv_IntVars(t *testing.T) {
	cfg := &AppConfig{FeedbackAnonScreenshotsPerHour: 20, FeedbackRetentionDays: 90}

	t.Setenv(EnvAppFeedbackAnonScreenshotsPerHour, "5")
	t.Setenv(EnvAppFeedbackRetentionDays, "not-a-number")
	applyFeedbackEnv(cfg)

	require.Equal(t, 5, cfg.FeedbackAnonScreenshotsPerHour)
	require.Equal(t, 90, cfg.FeedbackRetentionDays, "an unparseable value leaves the setting alone")

	t.Setenv(EnvAppFeedbackRetentionDays, "0")
	applyFeedbackEnv(cfg)
	require.Zero(t, cfg.FeedbackRetentionDays)
}

func TestLoad_FeedbackDefaults(t *testing.T) {
	t.Setenv(EnvAppFeedbackAnonScreenshotsPerHour, "")
	t.Setenv(EnvAppFeedbackRetentionDays, "")

	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, DefaultFeedbackAnonScreenshotsPerHour, cfg.App.FeedbackAnonScreenshotsPerHour)
	require.Equal(t, DefaultFeedbackRetentionDays, cfg.App.FeedbackRetentionDays)
}
