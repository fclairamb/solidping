package checktypes

import (
	"errors"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/checkers/schemas"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// schemaCacheControl lets a client hold a schema for a day. The document only
// changes with a release, and an editor that fetches one per keystroke is the
// obvious failure mode of a completion source.
const schemaCacheControl = "public, max-age=86400"

// GetConfigSchema handles GET /api/v1/checks/schema/:type (public, no auth).
//
// It serves the generated JSON Schema of a check type's `config` object, as
// `application/schema+json`. The document is a description for editors and
// third-party tooling, NOT the validator: SolidPing validates a config with the
// Go `Validate()` of its checker, which enforces formats, bounds and cross-field
// rules a reflected schema cannot express. Each schema repeats that in its own
// `description` / `x-solidping-validation`, and lists what it knowingly does not
// encode in `x-solidping-notes`. Use `POST /orgs/:org/checks/validate` (or
// `sp checks validate`) to find out whether a config is actually accepted.
//
// Public because it carries no organization data — it describes this build's
// check types, exactly like `GET /api/v1/check-types`.
func (h *Handler) GetConfigSchema(writer http.ResponseWriter, req *http.Request) error {
	checkType := httpx.Param(req, "type")

	raw, err := schemas.Get(checkType)
	if err != nil {
		if errors.Is(err, schemas.ErrNotFound) {
			return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound,
				"No config schema for check type "+checkType)
		}

		return h.WriteInternalError(writer, req, err)
	}

	writer.Header().Set("Content-Type", schemas.MediaType)
	writer.Header().Set("Cache-Control", schemaCacheControl)
	writer.WriteHeader(http.StatusOK)

	if _, err := writer.Write(raw); err != nil {
		// The status line is already out, so there is nothing left to tell the
		// client. Returning the error lets the logging middleware record it.
		return err //nolint:wrapcheck // a write failure on a flushed response has no useful wrapping
	}

	return nil
}

// ListConfigSchemas handles GET /api/v1/checks/schema (public, no auth): the
// catalog, so a consumer can discover the per-type URLs without guessing type
// names. Wrapped in `{ "data": [...] }` like every other list response.
func (h *Handler) ListConfigSchemas(writer http.ResponseWriter, _ *http.Request) error {
	types := schemas.Types()

	data := make([]ConfigSchemaRef, 0, len(types))
	for _, checkType := range types {
		data = append(data, ConfigSchemaRef{
			CheckType: checkType,
			Ref:       "/api/v1/checks/schema/" + checkType,
		})
	}

	return h.WriteJSON(writer, http.StatusOK, ListConfigSchemasResponse{
		Data: data,
		Note: schemaDescriptionOnlyNote,
	})
}

// schemaDescriptionOnlyNote travels with the catalog for the same reason it is
// stamped into every schema file: the one mistake worth preventing is a CI gate
// wired onto this JSON instead of the Go validator.
const schemaDescriptionOnlyNote = "These schemas describe a check config for editors and tooling. " +
	"They are not the validator — SolidPing validates with the Go Validate() of each checker " +
	"(`sp checks validate`, or POST /api/v1/orgs/{org}/checks/validate), which enforces rules " +
	"a reflected JSON Schema cannot express."

// ConfigSchemaRef points at one check type's config schema.
type ConfigSchemaRef struct {
	CheckType string `json:"checkType"`
	Ref       string `json:"ref"`
}

// ListConfigSchemasResponse is the catalog of published config schemas.
type ListConfigSchemasResponse struct {
	Data []ConfigSchemaRef `json:"data"`
	Note string            `json:"note"`
}
