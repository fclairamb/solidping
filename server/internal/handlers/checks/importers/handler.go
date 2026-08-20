package importers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// maxConvertBodySize caps the payload the convert endpoint will read (Gatus
// configs and Kuma backups are text; 16 MiB is generous).
const maxConvertBodySize = 16 << 20

// queryTrue is the literal a boolean query flag must equal to be enabled.
const queryTrue = "true"

// ContextConverter is implemented by converters that perform I/O and therefore
// need the request context (Better Stack).
type ContextConverter interface {
	Converter
	ConvertContext(ctx context.Context, input []byte) (*ConversionResult, error)
}

// ConvertResult is the convert endpoint's response: the existing apply/dry-run
// result shape, plus the source, how many checks the conversion produced, and
// the conversion warnings. Warnings raised by the apply itself are folded into
// the same array so callers only have one place to look.
type ConvertResult struct {
	Source    string                  `json:"source"`
	Converted int                     `json:"converted"`
	Manifest  string                  `json:"manifest"`
	DryRun    bool                    `json:"dryRun"`
	Created   int                     `json:"created"`
	Updated   int                     `json:"updated"`
	Unmanaged int                     `json:"unmanaged"`
	Plan      []checks.ApplyPlanEntry `json:"plan"`
	Errors    []checks.ImportError    `json:"errors"`
	Warnings  []ConversionWarning     `json:"warnings"`
}

// Handler exposes the third-party import conversion endpoint. It owns no state
// beyond the checks service: converters are pure, and the Better Stack token
// lives only inside a single request.
type Handler struct {
	base.HandlerBase
	svc *checks.Service
	// betterStackBaseURL overrides the Better Stack API root (tests, proxies).
	betterStackBaseURL string
}

// NewHandler builds the importers handler.
func NewHandler(service *checks.Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         service,
	}
}

// WithBetterStackBaseURL returns a copy of the handler pointed at another
// Better Stack API root. Used by tests and self-hosted proxy setups.
func (h *Handler) WithBetterStackBaseURL(baseURL string) *Handler {
	clone := *h
	clone.betterStackBaseURL = baseURL

	return &clone
}

// RegisterRoutes wires the convert endpoint onto an existing route group. The
// group is expected to already be admin-gated, like the export/import/apply
// routes it sits beside.
func (h *Handler) RegisterRoutes(group *httpx.Group) {
	group.POST("/import/convert", h.ConvertAndApply)
}

// converterFor resolves the converter for a source identifier.
func (h *Handler) converterFor(source string) (Converter, error) {
	switch source {
	case SourceGatus:
		return &GatusConverter{}, nil
	case SourceUptimeKuma:
		return &UptimeKumaConverter{}, nil
	case SourceBetterStack:
		return NewBetterStackConverter(BetterStackOptions{BaseURL: h.betterStackBaseURL}), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownSource, source)
	}
}

// ConvertAndApply handles
// POST /api/v1/orgs/:org/checks/import/convert?source=…&dryRun=…
//
// It converts the payload into the canonical export document and feeds it
// straight through the existing ApplyChecks path — there is no second import
// pipeline. Nothing about the request is persisted beyond the resulting checks;
// in particular the Better Stack API token is never stored or logged.
func (h *Handler) ConvertAndApply(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	source := req.URL.Query().Get("source")

	converter, err := h.converterFor(source)
	if err != nil {
		return h.WriteValidationError(writer, "Unsupported import source", []base.ValidationErrorField{
			{Name: "source", Message: fmt.Sprintf("must be one of %v", SupportedSources())},
		})
	}

	body, err := io.ReadAll(io.LimitReader(req.Body, maxConvertBodySize))
	if err != nil {
		return h.WriteValidationError(writer, "Invalid body", []base.ValidationErrorField{
			{Name: "body", Message: "could not read request body"},
		})
	}

	if len(body) == 0 {
		return h.WriteValidationError(writer, "Invalid body", []base.ValidationErrorField{
			{Name: "body", Message: "the request body is empty"},
		})
	}

	result, err := convert(req.Context(), converter, body)
	if err != nil {
		return h.writeConversionError(writer, req, err)
	}

	applyResult, err := h.svc.ApplyChecks(req.Context(), orgSlug, result.Document, checks.ApplyOptions{
		DryRun: req.URL.Query().Get("dryRun") == queryTrue,
	})
	if err != nil {
		switch {
		case errors.Is(err, checks.ErrOrganizationNotFound):
			return h.WriteErrorErr(
				writer, req, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found", err)
		default:
			return h.WriteErrorErr(
				writer, req, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error(), err)
		}
	}

	return h.WriteJSON(writer, http.StatusOK, buildConvertResult(source, result, applyResult))
}

// convert runs the converter, preferring the context-aware variant when the
// converter performs I/O.
func convert(ctx context.Context, converter Converter, body []byte) (*ConversionResult, error) {
	if ctxConverter, ok := converter.(ContextConverter); ok {
		return ctxConverter.ConvertContext(ctx, body)
	}

	return converter.Convert(body)
}

// buildConvertResult merges the conversion warnings with the apply result.
func buildConvertResult(
	source string, conversion *ConversionResult, applyResult *checks.ApplyResult,
) ConvertResult {
	merged := make([]ConversionWarning, 0, len(conversion.Warnings)+len(applyResult.Warnings))
	merged = append(merged, conversion.Warnings...)

	for _, message := range applyResult.Warnings {
		merged = append(merged, ConversionWarning{Item: "apply", Message: message})
	}

	return ConvertResult{
		Source:    source,
		Converted: len(conversion.Document.Checks),
		Manifest:  applyResult.Manifest,
		DryRun:    applyResult.DryRun,
		Created:   applyResult.Created,
		Updated:   applyResult.Updated,
		Unmanaged: applyResult.Unmanaged,
		Plan:      applyResult.Plan,
		Errors:    applyResult.Errors,
		Warnings:  merged,
	}
}

// writeConversionError translates a converter failure into the standard error
// shape. Every branch is a VALIDATION_ERROR-family response: the payload (or
// the credential it carries) is the caller's to fix, and the message never
// echoes the credential back.
func (h *Handler) writeConversionError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrBetterStackTokenRequired):
		return h.WriteValidationError(writer, "A Better Stack API token is required",
			[]base.ValidationErrorField{{Name: "token", Message: "must not be empty"}})
	case errors.Is(err, ErrBetterStackUnauthorized):
		return h.WriteErrorErr(writer, request, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Better Stack rejected the API token", err)
	case errors.Is(err, ErrBetterStackUnreachable), errors.Is(err, ErrBetterStackAPI):
		return h.WriteErrorErr(writer, request, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Could not read the Better Stack account: "+err.Error(), err)
	default:
		return h.WriteErrorErr(writer, request, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error(), err)
	}
}
