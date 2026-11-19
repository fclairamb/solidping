package incidentpublications_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentpublications"
)

// TestPublicationTranslationsCreateAndPatch: create with a translation, read it
// back (and on the public projection), retitle keeps it and drops the recipe,
// a new map replaces it, {} and null clear it.
func TestPublicationTranslationsCreateAndPatch(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newPubSetup(t, setupOptions{})
	ctx := t.Context()

	created, err := s.pubs.CreatePublication(ctx, s.org.Slug, s.page.UID, s.userUID,
		&incidentpublications.CreatePublicationRequest{
			Title:        "Payments are slow",
			Translations: models.Translations{"fr": {Title: "Paiements lents"}},
		})
	r.NoError(err)
	r.Equal("Paiements lents", created.Translations["fr"].Title)

	got, err := s.pubs.GetPublication(ctx, s.org.Slug, s.page.UID, created.UID)
	r.NoError(err)
	r.Equal("Paiements lents", got.Translations["fr"].Title)
	r.Contains(s.publicJSON(), `"translations":{"fr":{"title":"Paiements lents"}}`)

	// The row still has a recipe, as a templated title would.
	r.NoError(s.dbSvc.UpdateIncidentPublication(ctx, created.UID, &models.IncidentPublicationUpdate{}))

	retitle := "Payments are very slow"
	patched, err := s.pubs.UpdatePublication(ctx, s.org.Slug, s.page.UID, created.UID, s.userUID,
		&incidentpublications.UpdatePublicationRequest{Title: &retitle})
	r.NoError(err)
	r.Equal("Paiements lents", patched.Translations["fr"].Title, "a retitle keeps the translations")
	r.Nil(patched.TitleI18n)

	replaced, err := s.pubs.UpdatePublication(ctx, s.org.Slug, s.page.UID, created.UID, s.userUID,
		&incidentpublications.UpdatePublicationRequest{Translations: models.TranslationsPatch{
			Set: true, Value: models.Translations{"de": {Title: "Zahlungen langsam"}},
		}})
	r.NoError(err)
	r.Len(replaced.Translations, 1)
	r.Equal("Zahlungen langsam", replaced.Translations["de"].Title)

	for _, clearing := range []models.Translations{{}, nil} {
		again, err := s.pubs.UpdatePublication(ctx, s.org.Slug, s.page.UID, created.UID, s.userUID,
			&incidentpublications.UpdatePublicationRequest{
				Translations: models.TranslationsPatch{Set: true, Value: clearing},
			})
		r.NoError(err)
		r.Empty(again.Translations)

		row, err := s.dbSvc.GetIncidentPublication(ctx, s.org.UID, created.UID)
		r.NoError(err)
		r.Nil(row.Translations)
	}
}

// TestPublicationTranslationsValidation: each invalid shape is a validation
// error on create and on patch.
func TestPublicationTranslationsValidation(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		tr   models.Translations
		want error
	}{
		"unknown language": {models.Translations{"xx": {Title: "t"}}, models.ErrTranslationLanguage},
		"empty title":      {models.Translations{"fr": {Title: ""}}, models.ErrTranslationTitleRequired},
		"oversized title": {
			models.Translations{"fr": {Title: strings.Repeat("a", 201)}}, models.ErrTranslationTitleTooLong,
		},
		"page's own language": {models.Translations{"en": {Title: "t"}}, models.ErrTranslationOwnLanguage},
		"body not allowed":    {models.Translations{"fr": {Title: "t", Body: "b"}}, models.ErrTranslationBodyNotAllowed},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			s := newPubSetup(t, setupOptions{})
			ctx := t.Context()

			_, err := s.pubs.CreatePublication(ctx, s.org.Slug, s.page.UID, s.userUID,
				&incidentpublications.CreatePublicationRequest{Title: "Slow", Translations: tc.tr})
			r.ErrorIs(err, tc.want)

			created, err := s.pubs.CreatePublication(ctx, s.org.Slug, s.page.UID, s.userUID,
				&incidentpublications.CreatePublicationRequest{Title: "Slow"})
			r.NoError(err)

			_, err = s.pubs.UpdatePublication(ctx, s.org.Slug, s.page.UID, created.UID, s.userUID,
				&incidentpublications.UpdatePublicationRequest{
					Translations: models.TranslationsPatch{Set: true, Value: tc.tr},
				})
			r.ErrorIs(err, tc.want)
		})
	}
}

// TestHandlerPublicationTranslations: camelCase in the JSON, PATCH presence
// semantics, and a bad payload answers VALIDATION_ERROR.
func TestHandlerPublicationTranslations(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newPubHandlerEnv(t)

	rec := env.do(t, http.MethodPost, env.pagesPath(),
		`{"title":"Slow","translations":{"fr":{"title":"Lent"}}}`)
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var created struct {
		UID          string                       `json:"uid"`
		Translations map[string]map[string]string `json:"translations"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &created))
	r.Equal("Lent", created.Translations["fr"]["title"])

	path := env.pagesPath() + "/" + created.UID

	rec = env.do(t, http.MethodPatch, path, `{"severity":"minor"}`)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	r.Contains(rec.Body.String(), `"translations":{"fr":{"title":"Lent"}}`, "absent key leaves it untouched")

	rec = env.do(t, http.MethodPatch, path, `{"translations":{"xx":{"title":"t"}}}`)
	r.Equal(http.StatusUnprocessableEntity, rec.Code)
	r.Contains(rec.Body.String(), "VALIDATION_ERROR")

	rec = env.do(t, http.MethodPost, env.pagesPath(), `{"title":"Slow","translations":{"fr":{"title":""}}}`)
	r.Equal(http.StatusUnprocessableEntity, rec.Code)
	r.Contains(rec.Body.String(), "VALIDATION_ERROR")

	rec = env.do(t, http.MethodPatch, path, `{"translations":null}`)
	r.Equal(http.StatusOK, rec.Code)
	r.NotContains(rec.Body.String(), "translations")
}
