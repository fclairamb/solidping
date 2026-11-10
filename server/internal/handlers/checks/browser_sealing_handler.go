package checks

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/sealing"
)

// Error codes of browser-sealed secrets (spec 2026-11-08-01).
const (
	errorCodeSealedSecretsRequirePrivateRegions base.ErrorCode = "SEALED_SECRETS_REQUIRE_PRIVATE_REGIONS"
	errorCodeSecretsMustBeSealed                base.ErrorCode = "SECRETS_MUST_BE_SEALED"
	errorCodeSealingRecipientsChanged           base.ErrorCode = "SEALING_RECIPIENTS_CHANGED"
)

// writeSealingError translates the browser-sealing errors. The first return
// reports whether err was one of them.
func (h *Handler) writeSealingError(writer http.ResponseWriter, request *http.Request, err error) (bool, error) {
	var changed *SealingRecipientsChangedError

	switch {
	case errors.As(err, &changed):
		recipients := changed.Recipients
		if recipients == nil {
			recipients = []sealing.Recipient{}
		}

		return true, h.WriteJSON(writer, http.StatusConflict, map[string]any{
			"title":      "Sealing recipients changed",
			"code":       string(errorCodeSealingRecipientsChanged),
			"detail":     changed.Error(),
			"recipients": recipients,
		})
	case errors.Is(err, ErrSealedSecretsRequirePrivateRegions):
		return true, h.WriteErrorErr(writer, request, http.StatusBadRequest,
			errorCodeSealedSecretsRequirePrivateRegions, err.Error(), err)
	case errors.Is(err, ErrSecretsMustBeSealed):
		return true, h.WriteErrorErr(writer, request, http.StatusBadRequest,
			errorCodeSecretsMustBeSealed, err.Error(), err)
	case errors.Is(err, ErrSealedSecretsInvalid):
		return true, h.WriteErrorErr(writer, request, http.StatusBadRequest,
			base.ErrorCodeValidationError, err.Error(), err)
	default:
		return false, nil
	}
}

// writeConfigOrSealingError translates a checker config error or a
// browser-sealing error. The first return reports whether err was one.
func (h *Handler) writeConfigOrSealingError(
	writer http.ResponseWriter, request *http.Request, err error,
) (bool, error) {
	if configErr := checkerdef.IsConfigError(err); configErr != nil {
		return true, h.WriteValidationError(writer, "Configuration validation failed", []base.ValidationErrorField{
			{Name: configErr.Parameter, Message: configErr.Message},
		})
	}

	return h.writeSealingError(writer, request, err)
}

// SealingRecipients handles GET /orgs/:org/sealing-recipients?region=@a,@b.
func (h *Handler) SealingRecipients(writer http.ResponseWriter, req *http.Request) error {
	var regionList []string

	for _, region := range strings.Split(req.URL.Query().Get("region"), ",") {
		if region = strings.TrimSpace(region); region != "" {
			regionList = append(regionList, region)
		}
	}

	resp, err := h.svc.SealingRecipients(req.Context(), httpx.Param(req, "org"), regionList)
	if err != nil {
		switch {
		case errors.Is(err, ErrOrganizationNotFound):
			return h.WriteErrorErr(writer, req, http.StatusNotFound,
				base.ErrorCodeOrganizationNotFound, "Organization not found", err)
		case errors.Is(err, ErrSealedSecretsRequirePrivateRegions):
			return h.WriteErrorErr(writer, req, http.StatusBadRequest, base.ErrorCodeValidationError,
				"region must list private regions of this organization", err)
		default:
			return h.WriteInternalError(writer, req, err)
		}
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// NonCompliantChecks handles GET /orgs/:org/sealing/noncompliant-checks: the
// checks a `required` org still has to fix.
func (h *Handler) NonCompliantChecks(writer http.ResponseWriter, req *http.Request) error {
	checks, err := h.svc.ListNonCompliantChecks(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		if errors.Is(err, ErrOrganizationNotFound) {
			return h.WriteErrorErr(writer, req, http.StatusNotFound,
				base.ErrorCodeOrganizationNotFound, "Organization not found", err)
		}

		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{"data": checks})
}

// ResealChecks handles GET /orgs/:org/sealing/reseal-checks: the checks
// flagged `needsReseal`, listed by the Re-seal page.
func (h *Handler) ResealChecks(writer http.ResponseWriter, req *http.Request) error {
	checks, err := h.svc.ListResealChecks(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		if errors.Is(err, ErrOrganizationNotFound) {
			return h.WriteErrorErr(writer, req, http.StatusNotFound,
				base.ErrorCodeOrganizationNotFound, "Organization not found", err)
		}

		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{"data": checks})
}

// errorCodeRewrapUnavailable is a server without the peer re-wrap kind.
const errorCodeRewrapUnavailable base.ErrorCode = "REWRAP_UNAVAILABLE"

// writeRewrapError translates the errors of the peer re-wrap endpoints.
func (h *Handler) writeRewrapError(writer http.ResponseWriter, req *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrOrganizationNotFound):
		return h.WriteErrorErr(writer, req, http.StatusNotFound,
			base.ErrorCodeOrganizationNotFound, "Organization not found", err)
	case errors.Is(err, ErrCheckNotFound):
		return h.WriteErrorErr(writer, req, http.StatusNotFound, base.ErrorCodeCheckNotFound, "Check not found", err)
	case errors.Is(err, ErrSealedSecretsRequirePrivateRegions):
		return h.WriteErrorErr(writer, req, http.StatusBadRequest, base.ErrorCodeValidationError,
			"region must be a private region of this organization", err)
	case errors.Is(err, ErrRewrapUnavailable):
		return h.WriteErrorErr(writer, req, http.StatusServiceUnavailable, errorCodeRewrapUnavailable, err.Error(), err)
	default:
		return h.WriteInternalError(writer, req, err)
	}
}

// StartRewrap handles POST /orgs/:org/sealing/rewrap (spec 2026-11-10-03):
// re-seal the flagged checks of a region (or the given checks) on an online
// agent that can open them. Each check is reported: started, or why not.
func (h *Handler) StartRewrap(writer http.ResponseWriter, req *http.Request) error {
	var body RewrapRequest
	if req.ContentLength != 0 {
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
				{Name: fieldBody, Message: msgInvalidJSON},
			})
		}
	}

	outcomes, err := h.svc.StartRewrap(req.Context(), httpx.Param(req, "org"), &body)
	if err != nil {
		return h.writeRewrapError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{"data": outcomes})
}

// RewrapStatus handles GET /orgs/:org/sealing/rewrap?check=<uid>,<uid>: the
// latest re-wrap of each check (of every check with a recent one without
// `check`). A re-wrap left unfinished for rewraptask.Timeout reads as failed
// with `timed_out`.
func (h *Handler) RewrapStatus(writer http.ResponseWriter, req *http.Request) error {
	var checkUIDs []string

	for _, uid := range strings.Split(req.URL.Query().Get("check"), ",") {
		if uid = strings.TrimSpace(uid); uid != "" {
			checkUIDs = append(checkUIDs, uid)
		}
	}

	outcomes, err := h.svc.RewrapStatus(req.Context(), httpx.Param(req, "org"), checkUIDs)
	if err != nil {
		return h.writeRewrapError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{"data": outcomes})
}
