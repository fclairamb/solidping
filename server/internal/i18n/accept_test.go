package i18n_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/i18n"
)

func TestFromAcceptLanguage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("fr", i18n.FromAcceptLanguage("fr-FR,fr;q=0.9,en;q=0.8"))
	r.Equal("de", i18n.FromAcceptLanguage("ja, de;q=0.5"))
	r.Empty(i18n.FromAcceptLanguage("ja"))
	r.Empty(i18n.FromAcceptLanguage(""))
	r.Empty(i18n.FromAcceptLanguage(";;;=="))
}

func TestLocaleHas(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	loc := i18n.New("fr", "")
	r.True(loc.Has("feed.types.incident.created"))
	r.False(loc.Has("feed.types.incident.nope"))
}
