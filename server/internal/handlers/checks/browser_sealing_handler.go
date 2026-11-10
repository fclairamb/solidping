package checks

import (
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
