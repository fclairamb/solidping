package testapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

const (
	maxBulkCount  = 10000
	defaultPeriod = 30 * time.Second
	defaultOrg    = "test"
	nbPlaceholder = "{nb}"
	maxBulkErrors = 10
)

var (
	// ErrBulkTypeRequired is returned when type parameter is missing.
	ErrBulkTypeRequired = errors.New("parameter 'type' is required")
	// ErrBulkSlugRequired is returned when slug parameter is missing.
	ErrBulkSlugRequired = errors.New("parameter 'slug' is required")
	// ErrBulkSlugPlaceholder is returned when slug doesn't contain {nb}.
	ErrBulkSlugPlaceholder = errors.New("parameter 'slug' must contain {nb}")
	// ErrBulkCountRequired is returned when count parameter is missing.
	ErrBulkCountRequired = errors.New("parameter 'count' is required")
	// ErrBulkCountRange is returned when count is outside valid range.
	ErrBulkCountRange = fmt.Errorf("parameter 'count' must be between 1 and %d", maxBulkCount)
	// ErrBulkOrgNotFound is returned when organization is not found.
	ErrBulkOrgNotFound = errors.New("organization not found")
)

// BulkCreateChecksResponse represents the response from bulk check creation.
type BulkCreateChecksResponse struct {
	Created   int      `json:"created"`
	Failed    int      `json:"failed"`
	Errors    []string `json:"errors,omitempty"`
	FirstSlug string   `json:"firstSlug,omitempty"`
	LastSlug  string   `json:"lastSlug,omitempty"`
}

// BulkDeleteChecksResponse represents the response from bulk check deletion.
type BulkDeleteChecksResponse struct {
	Deleted int `json:"deleted"`
}

// bulkCreateParams holds parsed parameters for bulk check creation.
type bulkCreateParams struct {
	checkType    string
	slugTemplate string
	nameTemplate string
	urlTemplate  string
	period       time.Duration
	count        int
	orgUID       string
}

// BulkCreateChecks creates multiple checks from a template.
func (h *Handler) BulkCreateChecks(writer http.ResponseWriter, req bunrouter.Request) error {
	params, err := h.parseBulkCreateParams(req)
	if err != nil {
		return h.writeError(writer, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
	}

	checker, ok := registry.GetChecker(checkerdef.CheckType(params.checkType))
	if !ok {
		return h.writeError(writer, http.StatusBadRequest, "VALIDATION_ERROR",
			fmt.Sprintf("Unknown check type '%s'", params.checkType))
	}

	resp := h.executeBulkCreate(req.Context(), params, checker)

	return h.writeJSON(writer, http.StatusOK, resp)
}

func (h *Handler) parseBulkCreateParams(req bunrouter.Request) (*bulkCreateParams, error) {
	query := req.URL.Query()

	checkType := query.Get("type")
	if checkType == "" {
		return nil, ErrBulkTypeRequired
	}

	slugTemplate := query.Get("slug")
	if slugTemplate == "" {
		return nil, ErrBulkSlugRequired
	}

	if !strings.Contains(slugTemplate, nbPlaceholder) {
		return nil, ErrBulkSlugPlaceholder
	}

	count, err := parseBulkCount(query.Get("count"))
	if err != nil {
		return nil, err
	}

	nameTemplate := query.Get("name")
	if nameTemplate == "" {
		nameTemplate = slugTemplate
	}

	period := defaultPeriod
	if periodStr := query.Get("period"); periodStr != "" {
		parsed, parseErr := time.ParseDuration(periodStr)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid period '%s': %w", periodStr, parseErr)
		}

		period = parsed
	}

	orgSlug := query.Get("org")
	if orgSlug == "" {
		orgSlug = defaultOrg
	}

	org, err := h.dbService.GetOrganizationBySlug(req.Context(), orgSlug)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrBulkOrgNotFound, orgSlug)
	}

	return &bulkCreateParams{
		checkType:    checkType,
		slugTemplate: slugTemplate,
		nameTemplate: nameTemplate,
		urlTemplate:  query.Get("url"),
		period:       period,
		count:        count,
		orgUID:       org.UID,
	}, nil
}

func (h *Handler) executeBulkCreate(
	ctx context.Context,
	params *bulkCreateParams,
	checker checkerdef.Checker,
) BulkCreateChecksResponse {
	resp := BulkCreateChecksResponse{}

	for i := range params.count {
		number := strconv.Itoa(i)
		slug := strings.ReplaceAll(params.slugTemplate, nbPlaceholder, number)
		name := strings.ReplaceAll(params.nameTemplate, nbPlaceholder, number)

		check := models.NewCheck(params.orgUID, slug, params.checkType)
		check.Name = &name
		check.Period = timeutils.Duration(params.period)

		config := make(map[string]any)
		if params.urlTemplate != "" {
			config["url"] = strings.ReplaceAll(params.urlTemplate, nbPlaceholder, number)
		}

		check.Config = config

		spec := &checkerdef.CheckSpec{Name: name, Slug: slug, Period: params.period, Config: config}
		if validErr := checker.Validate(spec); validErr != nil {
			resp.Failed++
			if len(resp.Errors) < maxBulkErrors {
				resp.Errors = append(resp.Errors,
					fmt.Sprintf("check %d (%s): validation: %v", i, slug, validErr))
			}

			continue
		}

		if createErr := h.dbService.CreateCheck(ctx, check); createErr != nil {
			resp.Failed++
			if len(resp.Errors) < maxBulkErrors {
				resp.Errors = append(resp.Errors,
					fmt.Sprintf("check %d (%s): create: %v", i, slug, createErr))
			}

			continue
		}

		resp.Created++
		if resp.Created == 1 {
			resp.FirstSlug = slug
		}

		resp.LastSlug = slug
	}

	if h.eventNotifier != nil && resp.Created > 0 {
		if notifyErr := h.eventNotifier.Notify(ctx, string(models.EventTypeCheckCreated), "{}"); notifyErr != nil {
			slog.InfoContext(ctx, "Failed to notify check runners (non-fatal)", "error", notifyErr)
		}
	}

	return resp
}

// BulkDeleteChecks deletes multiple checks matching a slug template.
func (h *Handler) BulkDeleteChecks(writer http.ResponseWriter, req bunrouter.Request) error {
	ctx := req.Context()
	query := req.URL.Query()

	slugTemplate := query.Get("slug")
	if slugTemplate == "" {
		return h.writeError(writer, http.StatusBadRequest,
			"VALIDATION_ERROR", "Parameter 'slug' is required")
	}

	if !strings.Contains(slugTemplate, nbPlaceholder) {
		return h.writeError(writer, http.StatusBadRequest,
			"VALIDATION_ERROR", "Parameter 'slug' must contain {nb}")
	}

	count, err := parseBulkCount(query.Get("count"))
	if err != nil {
		return h.writeError(writer, http.StatusBadRequest,
			"VALIDATION_ERROR", err.Error())
	}

	orgSlug := query.Get("org")
	if orgSlug == "" {
		orgSlug = defaultOrg
	}

	org, err := h.dbService.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return h.writeError(writer, http.StatusBadRequest,
			"VALIDATION_ERROR", fmt.Sprintf("Organization '%s' not found", orgSlug))
	}

	resp := BulkDeleteChecksResponse{}

	for i := range count {
		slug := strings.ReplaceAll(slugTemplate, nbPlaceholder, strconv.Itoa(i))

		check, lookupErr := h.dbService.GetCheckByUidOrSlug(ctx, org.UID, slug)
		if lookupErr != nil {
			continue
		}

		if deleteErr := h.dbService.DeleteCheck(ctx, check.UID); deleteErr != nil {
			continue
		}

		resp.Deleted++
	}

	return h.writeJSON(writer, http.StatusOK, resp)
}

func parseBulkCount(countStr string) (int, error) {
	if countStr == "" {
		return 0, ErrBulkCountRequired
	}

	count, err := strconv.Atoi(countStr)
	if err != nil || count < 1 || count > maxBulkCount {
		return 0, ErrBulkCountRange
	}

	return count, nil
}
