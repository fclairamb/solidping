package statusupdates_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/statusupdates"
)

func frTranslation() models.Translations {
	return models.Translations{"fr": {Title: "Panne en cours", Body: "Nous enquêtons."}}
}

// TestTranslationsCreateAndPatch: create with translations, read them back,
// patch the literal title (translations kept, recipe cleared), then clear them.
func TestTranslationsCreateAndPatch(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := newTestSetup(t)
	ctx := t.Context()

	req := makeCreate(s.pageID, "investigating", "Outage", "We are looking")
	req.Translations = frTranslation()

	created, err := s.svc.CreateStatusUpdate(ctx, s.org.Slug, s.user.UID, req)
	r.NoError(err)
	r.Equal(frTranslation(), created.Translations)

	got, err := s.svc.GetStatusUpdate(ctx, s.org.Slug, created.UID)
	r.NoError(err)
	r.Equal(frTranslation(), got.Translations)

	// The row carries a recipe, as a machine-written one would.
	row, err := s.dbSvc.GetStatusUpdateByUID(ctx, created.UID)
	r.NoError(err)
	row.TitleI18n = models.NewLocalizedText("publication.openedTitle", &models.LocalizedParams{Name: "X"})
	r.NoError(s.dbSvc.UpdateStatusUpdate(ctx, row))

	title := "Major outage"
	patched, err := s.svc.UpdateStatusUpdate(ctx, s.org.Slug, created.UID, s.user.UID,
		&statusupdates.UpdateStatusUpdateRequest{Title: &title})
	r.NoError(err)
	r.Equal(frTranslation(), patched.Translations, "editing the literal text keeps the translations")
	r.Nil(patched.TitleI18n, "editing the literal title clears the recipe")

	// A new map replaces the old one entirely.
	replaced, err := s.svc.UpdateStatusUpdate(ctx, s.org.Slug, created.UID, s.user.UID,
		&statusupdates.UpdateStatusUpdateRequest{Translations: models.TranslationsPatch{
			Set: true, Value: models.Translations{"de": {Title: "Ausfall", Body: "Wir untersuchen."}},
		}})
	r.NoError(err)
	r.Len(replaced.Translations, 1)
	r.Equal("Ausfall", replaced.Translations["de"].Title)

	for _, clearing := range []models.Translations{{}, nil} {
		again, err := s.svc.UpdateStatusUpdate(ctx, s.org.Slug, created.UID, s.user.UID,
			&statusupdates.UpdateStatusUpdateRequest{Translations: models.TranslationsPatch{Set: true, Value: clearing}})
		r.NoError(err)
		r.Empty(again.Translations)
	}
}

// TestTranslationsValidation: every invalid shape is a validation error, on
// create and on patch.
func TestTranslationsValidation(t *testing.T) {
	t.Parallel()

	fr := "fr"

	cases := map[string]struct {
		pageLang *string
		tr       models.Translations
		want     error
	}{
		"unknown language": {nil, models.Translations{"xx": {Title: "t", Body: "b"}}, models.ErrTranslationLanguage},
		"region tag":       {nil, models.Translations{"fr-FR": {Title: "t", Body: "b"}}, models.ErrTranslationLanguage},
		"empty title":      {nil, models.Translations{"fr": {Title: "", Body: "b"}}, models.ErrTranslationTitleRequired},
		"title too long": {
			nil, models.Translations{"fr": {Title: strings.Repeat("a", 201), Body: "b"}}, models.ErrTranslationTitleTooLong,
		},
		"empty body with literal body": {nil, models.Translations{"fr": {Title: "t"}}, models.ErrTranslationBodyRequired},
		"oversized body": {
			nil, models.Translations{"fr": {Title: "t", Body: strings.Repeat("a", 16385)}}, models.ErrTranslationBodyTooLong,
		},
		"page's own language (default en)": {nil, models.Translations{"en": {Title: "t", Body: "b"}}, models.ErrTranslationOwnLanguage},
		"page's own language (fr)":         {&fr, models.Translations{"fr": {Title: "t", Body: "b"}}, models.ErrTranslationOwnLanguage},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			s := newTestSetup(t)
			ctx := t.Context()

			if tc.pageLang != nil {
				r.NoError(s.dbSvc.UpdateStatusPage(ctx, s.pageID, &models.StatusPageUpdate{Language: tc.pageLang}))
			}

			req := makeCreate(s.pageID, "info", "Title", "Body")
			req.Translations = tc.tr
			_, err := s.svc.CreateStatusUpdate(ctx, s.org.Slug, s.user.UID, req)
			r.ErrorIs(err, tc.want)

			created, err := s.svc.CreateStatusUpdate(ctx, s.org.Slug, s.user.UID, makeCreate(s.pageID, "info", "Title", "Body"))
			r.NoError(err)

			_, err = s.svc.UpdateStatusUpdate(ctx, s.org.Slug, created.UID, s.user.UID,
				&statusupdates.UpdateStatusUpdateRequest{Translations: models.TranslationsPatch{Set: true, Value: tc.tr}})
			r.ErrorIs(err, tc.want)
		})
	}
}

// TestHandlerTranslations: translations are in the JSON (camelCase), PATCH
// semantics for absent / {} / null, and a bad payload answers VALIDATION_ERROR.
func TestHandlerTranslations(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHandlerSetup(t)
	base := "/api/v1/orgs/" + h.org.Slug + "/status-updates"

	rec := h.do(t, http.MethodPost, base, map[string]any{
		"statusPageUid": h.pageID, "kind": "info", "title": "Notice", "bodyMarkdown": "Body",
		"translations": map[string]any{"fr": map[string]any{"title": "Avis", "body": "Corps"}},
	})
	r.Equal(http.StatusCreated, rec.Code)

	var created struct {
		UID          string                       `json:"uid"`
		Translations map[string]map[string]string `json:"translations"`
	}
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &created))
	r.Equal("Avis", created.Translations["fr"]["title"])

	// Absent: untouched.
	rec = h.do(t, http.MethodPatch, base+"/"+created.UID, map[string]any{"title": "Notice 2"})
	r.Equal(http.StatusOK, rec.Code)
	r.Contains(rec.Body.String(), `"translations":{"fr":{"title":"Avis","body":"Corps"}}`)

	// Bad payload.
	rec = h.do(t, http.MethodPatch, base+"/"+created.UID, map[string]any{
		"translations": map[string]any{"xx": map[string]any{"title": "t", "body": "b"}},
	})
	r.Equal(http.StatusUnprocessableEntity, rec.Code)
	r.Contains(rec.Body.String(), "VALIDATION_ERROR")

	rec = h.do(t, http.MethodPost, base, map[string]any{
		"statusPageUid": h.pageID, "kind": "info", "title": "Notice", "bodyMarkdown": "Body",
		"translations": map[string]any{"fr": map[string]any{"title": "", "body": "Corps"}},
	})
	r.Equal(http.StatusUnprocessableEntity, rec.Code)
	r.Contains(rec.Body.String(), "VALIDATION_ERROR")

	// null clears.
	rec = h.do(t, http.MethodPatch, base+"/"+created.UID, map[string]any{"translations": nil})
	r.Equal(http.StatusOK, rec.Code)
	r.NotContains(rec.Body.String(), "translations")
}
