package checkerdef_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
)

func TestActivationResolver_DefaultAllEnabled(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	resolver := checkerdef.NewActivationResolver(config.CheckersConfig{})
	enabled := resolver.ListEnabledTypes(nil)

	r.Len(enabled, len(checkerdef.ListCheckTypeMetas()))
	r.True(resolver.IsTypeEnabled(checkerdef.CheckTypeHTTP, nil))
	r.True(resolver.IsTypeEnabled(checkerdef.CheckTypeDocker, nil))
}

func TestActivationResolver_ExplicitAllowlist(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	resolver := checkerdef.NewActivationResolver(config.CheckersConfig{
		Enabled: []string{"http", "tcp"},
	})

	r.True(resolver.IsTypeEnabled(checkerdef.CheckTypeHTTP, nil))
	r.True(resolver.IsTypeEnabled(checkerdef.CheckTypeTCP, nil))
	r.False(resolver.IsTypeEnabled(checkerdef.CheckTypeDNS, nil))
	r.Len(resolver.ListEnabledTypes(nil), 2)
}

func TestActivationResolver_EnabledLabels(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	resolver := checkerdef.NewActivationResolver(config.CheckersConfig{
		EnabledLabels: []string{"safe"},
	})

	r.True(resolver.IsTypeEnabled(checkerdef.CheckTypeHTTP, nil))
	r.False(resolver.IsTypeEnabled(checkerdef.CheckTypeICMP, nil))
	r.False(resolver.IsTypeEnabled(checkerdef.CheckTypeDocker, nil))
	r.False(resolver.IsTypeEnabled(checkerdef.CheckTypeBrowser, nil))
	r.False(resolver.IsTypeEnabled(checkerdef.CheckTypeJS, nil))
}

func TestActivationResolver_DisabledList(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	resolver := checkerdef.NewActivationResolver(config.CheckersConfig{
		Disabled: []string{"docker", "js"},
	})

	r.True(resolver.IsTypeEnabled(checkerdef.CheckTypeHTTP, nil))
	r.False(resolver.IsTypeEnabled(checkerdef.CheckTypeDocker, nil))
	r.False(resolver.IsTypeEnabled(checkerdef.CheckTypeJS, nil))
}

func TestActivationResolver_OrgDisabled(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	resolver := checkerdef.NewActivationResolver(config.CheckersConfig{})

	r.True(resolver.IsTypeEnabled(checkerdef.CheckTypeMySQL, nil))
	r.False(resolver.IsTypeEnabled(checkerdef.CheckTypeMySQL, []string{"mysql"}))
}

func TestActivationResolver_ListAllWithStatus(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	resolver := checkerdef.NewActivationResolver(config.CheckersConfig{
		Disabled: []string{"docker"},
	})

	statuses := resolver.ListAllWithStatus([]string{"js"})

	// Check specific types by iterating
	for idx := range statuses {
		if statuses[idx].Type == checkerdef.CheckTypeDocker {
			r.False(statuses[idx].Enabled)
			r.Equal("server", statuses[idx].DisabledReason)
		}

		if statuses[idx].Type == checkerdef.CheckTypeJS {
			r.False(statuses[idx].Enabled)
			r.Equal("organization", statuses[idx].DisabledReason)
		}

		if statuses[idx].Type == checkerdef.CheckTypeHTTP {
			r.True(statuses[idx].Enabled)
			r.Empty(statuses[idx].DisabledReason)
		}
	}
}

func TestCheckTypeMeta_MatchesLabels(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	meta := checkerdef.GetCheckTypeMeta(checkerdef.CheckTypeHTTP)
	r.NotNil(meta)
	r.True(meta.MatchesLabels([]string{"safe"}))
	r.True(meta.MatchesLabels([]string{"standalone"}))
	r.False(meta.MatchesLabels([]string{"unsafe"}))
	r.False(meta.MatchesLabels([]string{"requires:chrome"}))
}

func TestGetCheckTypeMeta(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	meta := checkerdef.GetCheckTypeMeta(checkerdef.CheckTypePostgreSQL)
	r.NotNil(meta)
	r.Equal(checkerdef.CheckTypePostgreSQL, meta.Type)
	r.Contains(meta.Labels, "category:database")

	unknown := checkerdef.GetCheckTypeMeta("nonexistent")
	r.Nil(unknown)
}
