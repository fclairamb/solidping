// Package statusupdates provides HTTP handlers for status update management endpoints.
package statusupdates

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/statuspageassets"
)

// Service errors.
var (
	// ErrOrganizationNotFound is returned when the organization does not exist.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrStatusUpdateNotFound is returned when the status update does not exist.
	ErrStatusUpdateNotFound = errors.New("status update not found")
	// ErrStatusPageNotFound is returned when the referenced status page does not exist.
	ErrStatusPageNotFound = errors.New("status page not found")
	// ErrStatusPageForbidden is returned when the status page belongs to a different org.
	ErrStatusPageForbidden = errors.New("status page belongs to a different org")
	// ErrSectionNotFound is returned when the section does not exist on the page.
	ErrSectionNotFound = errors.New("section not found on status page")
	// ErrCheckNotFound is returned when the check resource is not on the page.
	ErrCheckNotFound = errors.New("check not found on status page")
	// ErrIncidentNotFound is returned when the incident does not exist.
	ErrIncidentNotFound = errors.New("incident not found")
	// ErrInvalidKind is returned when the kind is not one of the valid values.
	ErrInvalidKind = errors.New("invalid status update kind")
	// ErrTitleRequired is returned when the title is empty.
	ErrTitleRequired = errors.New("title is required")
	// ErrTitleTooLong is returned when the title exceeds 200 characters.
	ErrTitleTooLong = errors.New("title must be at most 200 characters")
	// ErrBodyRequired is returned when bodyMarkdown is empty.
	ErrBodyRequired = errors.New("body is required")
	// ErrBodyTooLong is returned when bodyMarkdown exceeds 16384 characters.
	ErrBodyTooLong = errors.New("body must be at most 16384 characters")
	// ErrInvalidLinkURL is returned when linkUrl is not a valid http/https URL.
	ErrInvalidLinkURL = errors.New("link URL must be a valid http or https URL")
	// ErrCheckUIDMismatch is returned when checkUID does not match the incident's check.
	ErrCheckUIDMismatch = errors.New("check does not match the incident's check")
	// ErrSectionUIDRequired is returned when sectionUid is explicitly set to "" on update —
	// "" is never a legal UID; send null to clear the field instead.
	ErrSectionUIDRequired = errors.New("sectionUid must not be empty; send null to clear it")
	// ErrCheckUIDRequired is returned when checkUid is explicitly set to "" on update —
	// "" is never a legal UID; send null to clear the field instead.
	ErrCheckUIDRequired = errors.New("checkUid must not be empty; send null to clear it")
	// ErrIncidentUIDRequired is returned when incidentUid is explicitly set to "" on update —
	// "" is never a legal UID; send null to clear the field instead.
	ErrIncidentUIDRequired = errors.New("incidentUid must not be empty; send null to clear it")
)

const (
	maxTitleLen = 200
	maxBodyLen  = 16384
)

// SubscriberNotifier fans a published status update out to confirmed status-page
// subscribers. It is an optional dependency: when nil, CreateStatusUpdate skips
// the fan-out. The concrete implementation lives in the statussubscribers
// package; this small interface avoids an import cycle.
type SubscriberNotifier interface {
	NotifyStatusUpdate(ctx context.Context, ev *SubscriberUpdateEvent)
}

// SubscriberUpdateEvent describes a freshly-published update for fan-out.
type SubscriberUpdateEvent struct {
	StatusPageUID       string
	IncidentUID         *string
	Kind                string
	Title               string
	BodyMarkdown        string
	LinkURL             *string
	PageName            string
	IncidentUpdateCount int
	// PageLogoURL is the STATUS PAGE's own logo (nil when it wears the
	// SolidPing one). Subscriber mail is branded by the page, never by the
	// organization behind it: a subscriber opted into the page.
	PageLogoURL *string
	// PageHideBranding is the page's white-label opt-in. True means the
	// subscriber mail carries no logo and no SolidPing attribution at all.
	PageHideBranding bool
}

// Service provides business logic for status update management.
type Service struct {
	db       db.Service
	notifier SubscriberNotifier
}

// NewService creates a new status updates service.
func NewService(dbService db.Service) *Service {
	return &Service{db: dbService}
}

// SetSubscriberNotifier wires the subscriber fan-out notifier. Optional —
// CreateStatusUpdate is a no-op for subscribers when this is never called.
func (s *Service) SetSubscriberNotifier(n SubscriberNotifier) {
	s.notifier = n
}

// StatusUpdateResponse represents a status update in API responses.
type StatusUpdateResponse struct {
	UID           string    `json:"uid"`
	StatusPageUID string    `json:"statusPageUid"`
	SectionUID    *string   `json:"sectionUid,omitempty"`
	CheckUID      *string   `json:"checkUid,omitempty"`
	IncidentUID   *string   `json:"incidentUid,omitempty"`
	Title         string    `json:"title"`
	BodyMarkdown  string    `json:"bodyMarkdown"`
	LinkURL       *string   `json:"linkUrl,omitempty"`
	Kind          string    `json:"kind"`
	PublishedAt   time.Time `json:"publishedAt"`
	AuthorUID     *string   `json:"authorUid,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// CreateStatusUpdateRequest represents a request to create a status update.
type CreateStatusUpdateRequest struct {
	StatusPageUID string     `json:"statusPageUid"`
	SectionUID    *string    `json:"sectionUid,omitempty"`
	CheckUID      *string    `json:"checkUid,omitempty"`
	IncidentUID   *string    `json:"incidentUid,omitempty"`
	Title         string     `json:"title"`
	BodyMarkdown  string     `json:"bodyMarkdown"`
	LinkURL       *string    `json:"linkUrl,omitempty"`
	Kind          string     `json:"kind"`
	PublishedAt   *time.Time `json:"publishedAt,omitempty"`
}

// UpdateStatusUpdateRequest represents a request to update a status update (all fields optional).
//
// SectionUID, CheckUID, IncidentUID and LinkURL are presence-aware nullable
// fields: a plain *string cannot tell an omitted key from an explicit `null`
// (both decode to nil), so the handler additionally probes the raw request
// body and populates the matching *Set flag. Semantics for each of the four:
//   - key absent (Set=false): leave the column untouched.
//   - key present, value null (Set=true, pointer nil): clear the column (NULL).
//   - key present, value "" (Set=true, pointer to ""): clear for LinkURL;
//     VALIDATION_ERROR for the three UID fields ("" is never a legal UID).
//   - key present, non-empty value: validate and set.
type UpdateStatusUpdateRequest struct {
	SectionUID  *string `json:"sectionUid,omitempty"`
	CheckUID    *string `json:"checkUid,omitempty"`
	IncidentUID *string `json:"incidentUid,omitempty"`
	LinkURL     *string `json:"linkUrl,omitempty"`
	// *Set fields are populated by the handler from the raw body, NOT from
	// JSON decode (a bare *string can't distinguish absent from null).
	SectionUIDSet  bool `json:"-"`
	CheckUIDSet    bool `json:"-"`
	IncidentUIDSet bool `json:"-"`
	LinkURLSet     bool `json:"-"`

	Title        *string    `json:"title,omitempty"`
	BodyMarkdown *string    `json:"bodyMarkdown,omitempty"`
	Kind         *string    `json:"kind,omitempty"`
	PublishedAt  *time.Time `json:"publishedAt,omitempty"`
}

// ListStatusUpdatesOptions holds query options for listing status updates.
type ListStatusUpdatesOptions struct {
	StatusPage string
	Section    string
	Check      string
	Incident   string
	Limit      int
	Offset     int
}

func toResponse(update *models.StatusUpdate) StatusUpdateResponse {
	return StatusUpdateResponse{
		UID:           update.UID,
		StatusPageUID: update.StatusPageUID,
		SectionUID:    update.SectionUID,
		CheckUID:      update.CheckUID,
		IncidentUID:   update.IncidentUID,
		Title:         update.Title,
		BodyMarkdown:  update.BodyMarkdown,
		LinkURL:       update.LinkURL,
		Kind:          string(update.Kind),
		PublishedAt:   update.PublishedAt,
		AuthorUID:     update.AuthorUID,
		CreatedAt:     update.CreatedAt,
		UpdatedAt:     update.UpdatedAt,
	}
}

func validateLinkURL(rawURL *string) error {
	if rawURL == nil || *rawURL == "" {
		return nil
	}

	parsed, err := url.Parse(*rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ErrInvalidLinkURL
	}

	return nil
}

func validateTitle(title string) error {
	if title == "" {
		return ErrTitleRequired
	}
	if len(title) > maxTitleLen {
		return ErrTitleTooLong
	}

	return nil
}

func validateBody(body string) error {
	if body == "" {
		return ErrBodyRequired
	}
	if len(body) > maxBodyLen {
		return ErrBodyTooLong
	}

	return nil
}

// ListStatusUpdates returns status updates for an org with filtering.
func (s *Service) ListStatusUpdates(
	ctx context.Context, orgSlug string, opts *ListStatusUpdatesOptions,
) ([]StatusUpdateResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	filter := models.StatusUpdatesFilter{
		StatusPageUID: opts.StatusPage,
		Limit:         opts.Limit,
		Offset:        opts.Offset,
	}

	if opts.Section != "" {
		filter.SectionUID = &opts.Section
	}

	if opts.Check != "" {
		filter.CheckUID = &opts.Check
	}

	if opts.Incident != "" {
		filter.IncidentUID = &opts.Incident
	}

	updates, err := s.db.ListStatusUpdates(ctx, org.UID, filter)
	if err != nil {
		return nil, err
	}

	responses := make([]StatusUpdateResponse, len(updates))
	for idx, update := range updates {
		responses[idx] = toResponse(update)
	}

	return responses, nil
}

// CreateStatusUpdate creates a new status update after validation.
//
//nolint:funlen // validation of optional fields requires many checks
func (s *Service) CreateStatusUpdate( //nolint:cyclop // validation requires checking multiple fields
	ctx context.Context, orgSlug, authorUID string, req *CreateStatusUpdateRequest,
) (StatusUpdateResponse, error) {
	kind := models.StatusUpdateKind(req.Kind)
	if !kind.IsValid() {
		return StatusUpdateResponse{}, ErrInvalidKind
	}

	if err := validateTitle(req.Title); err != nil {
		return StatusUpdateResponse{}, err
	}

	if err := validateBody(req.BodyMarkdown); err != nil {
		return StatusUpdateResponse{}, err
	}

	if err := validateLinkURL(req.LinkURL); err != nil {
		return StatusUpdateResponse{}, err
	}

	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return StatusUpdateResponse{}, ErrOrganizationNotFound
	}

	page, err := s.db.GetStatusPage(ctx, org.UID, req.StatusPageUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return StatusUpdateResponse{}, ErrStatusPageNotFound
		}

		return StatusUpdateResponse{}, err
	}

	if page.OrganizationUID != org.UID {
		return StatusUpdateResponse{}, ErrStatusPageForbidden
	}

	if req.SectionUID != nil && *req.SectionUID != "" {
		section, sectErr := s.db.GetStatusPageSection(ctx, page.UID, *req.SectionUID)
		if sectErr != nil || section == nil {
			return StatusUpdateResponse{}, ErrSectionNotFound
		}
	}

	checkUID := req.CheckUID
	if checkUID != nil && *checkUID != "" {
		if err := s.validateCheckOnPage(ctx, page.UID, *checkUID); err != nil {
			return StatusUpdateResponse{}, err
		}
	}

	if req.IncidentUID != nil && *req.IncidentUID != "" {
		resolved, err := s.resolveIncidentCheck(ctx, org.UID, *req.IncidentUID, checkUID)
		if err != nil {
			return StatusUpdateResponse{}, err
		}

		checkUID = resolved
	}

	update := models.NewStatusUpdate(org.UID, page.UID, authorUID)
	update.Kind = kind
	update.Title = req.Title
	update.BodyMarkdown = req.BodyMarkdown
	update.LinkURL = req.LinkURL
	update.CheckUID = checkUID
	update.IncidentUID = req.IncidentUID

	if req.SectionUID != nil && *req.SectionUID != "" {
		update.SectionUID = req.SectionUID
	}

	if req.PublishedAt != nil {
		update.PublishedAt = *req.PublishedAt
	}

	if err := s.db.CreateStatusUpdate(ctx, update); err != nil {
		return StatusUpdateResponse{}, err
	}

	event := models.NewEvent(org.UID, models.EventTypeStatusUpdateCreated, models.ActorTypeUser)
	event.ActorUID = &authorUID
	event.Payload["status_update_uid"] = update.UID
	_ = s.db.CreateEvent(ctx, event)

	s.dispatchSubscriberNotification(ctx, page, update)

	return toResponse(update), nil
}

// dispatchSubscriberNotification fans the published update out to confirmed
// subscribers. It runs in a fire-and-forget goroutine so a slow or failing mail
// path never blocks the HTTP response or fails the status update. The detached
// goroutine uses a context derived via WithoutCancel because the request
// context is canceled once the handler returns.
func (s *Service) dispatchSubscriberNotification(
	ctx context.Context, page *models.StatusPage, update *models.StatusUpdate,
) {
	if s.notifier == nil {
		return
	}

	// Count prior updates for the incident (excluding this one) so the notifier
	// can pick the incident-opened template for the first update. Computed on
	// the request goroutine where the DB context is still valid.
	priorCount := 0

	if update.IncidentUID != nil && *update.IncidentUID != "" {
		existing, err := s.db.ListStatusUpdates(ctx, page.OrganizationUID, models.StatusUpdatesFilter{
			StatusPageUID: page.UID,
			IncidentUID:   update.IncidentUID,
			Limit:         200,
		})
		if err == nil {
			priorCount = len(existing) - 1 // exclude the just-created update
			if priorCount < 0 {
				priorCount = 0
			}
		}
	}

	event := &SubscriberUpdateEvent{
		StatusPageUID:       page.UID,
		IncidentUID:         update.IncidentUID,
		Kind:                string(update.Kind),
		Title:               update.Title,
		BodyMarkdown:        update.BodyMarkdown,
		LinkURL:             update.LinkURL,
		PageName:            page.Name,
		IncidentUpdateCount: priorCount,
		PageLogoURL:         statuspageassets.PublicURL(page.Settings.LogoFileUID()),
		PageHideBranding:    page.Settings.HideBranding(),
	}

	// Detach from the request context (preserving its values) so the goroutine
	// survives the handler returning, without inheriting its cancellation.
	bgCtx := context.WithoutCancel(ctx)

	go s.notifier.NotifyStatusUpdate(bgCtx, event)
}

// resolveIncidentCheck looks up the incident and returns the check UID to use,
// auto-filling from the incident when checkUID is not set.
func (s *Service) resolveIncidentCheck(
	ctx context.Context, orgUID, incidentUID string, checkUID *string,
) (*string, error) {
	incident, err := s.db.GetIncident(ctx, orgUID, incidentUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrIncidentNotFound
		}

		return nil, err
	}

	if incident.CheckUID == "" {
		return checkUID, nil
	}

	if checkUID != nil && *checkUID != "" && *checkUID != incident.CheckUID {
		return nil, ErrCheckUIDMismatch
	}

	if checkUID == nil || *checkUID == "" {
		return &incident.CheckUID, nil
	}

	return checkUID, nil
}

// GetStatusUpdate retrieves a single status update by UID, scoped to the org.
func (s *Service) GetStatusUpdate(
	ctx context.Context, orgSlug, uid string,
) (StatusUpdateResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return StatusUpdateResponse{}, ErrOrganizationNotFound
	}

	update, err := s.db.GetStatusUpdateByUID(ctx, uid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return StatusUpdateResponse{}, ErrStatusUpdateNotFound
		}

		return StatusUpdateResponse{}, err
	}

	if update.OrganizationUID != org.UID {
		return StatusUpdateResponse{}, ErrStatusUpdateNotFound
	}

	return toResponse(update), nil
}

// clearIfEmpty treats an explicit empty string as "clear" (nil) — a browser
// input yields "" rather than null, so linkUrl's clear affordance must accept
// both. A nil pointer (already meaning "clear") passes through unchanged.
func clearIfEmpty(value *string) *string {
	if value != nil && *value == "" {
		return nil
	}

	return value
}

// requireNonEmptyUID normalizes a presence-aware UID field: nil means "clear"
// (returned as nil), a non-nil pointer to "" is never a legal UID and is
// rejected with requiredErr (send null to clear instead), and a non-nil
// pointer to a real value passes through for the caller to validate.
func requireNonEmptyUID(value *string, requiredErr error) (*string, error) {
	if value == nil {
		return nil, nil //nolint:nilnil // nil value + nil error is "clear the field", not a failure
	}

	if *value == "" {
		return nil, requiredErr
	}

	return value, nil
}

// loadPageForUpdateValidation fetches the status page owning update, scoped to
// orgUID, for validating a set-a-value sectionUid/checkUid against it.
func (s *Service) loadPageForUpdateValidation(
	ctx context.Context, orgUID string, update *models.StatusUpdate,
) (*models.StatusPage, error) {
	page, err := s.db.GetStatusPage(ctx, orgUID, update.StatusPageUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrStatusPageNotFound
		}

		return nil, err
	}

	if page.OrganizationUID != orgUID {
		return nil, ErrStatusPageForbidden
	}

	return page, nil
}

// applySectionUpdate handles the presence-aware sectionUid field: nil clears
// it, a non-empty value is validated as a section of page.
func (s *Service) applySection(
	ctx context.Context, page *models.StatusPage, update *models.StatusUpdate, req *UpdateStatusUpdateRequest,
) error {
	sectionUID, err := requireNonEmptyUID(req.SectionUID, ErrSectionUIDRequired)
	if err != nil {
		return err
	}

	if sectionUID != nil {
		section, sectErr := s.db.GetStatusPageSection(ctx, page.UID, *sectionUID)
		if sectErr != nil || section == nil {
			return ErrSectionNotFound
		}
	}

	update.SectionUID = sectionUID

	return nil
}

// applyCheck handles the presence-aware checkUid field: nil clears it, a
// non-empty value is validated as a check resource of page.
func (s *Service) applyCheck(
	ctx context.Context, page *models.StatusPage, update *models.StatusUpdate, req *UpdateStatusUpdateRequest,
) error {
	checkUID, err := requireNonEmptyUID(req.CheckUID, ErrCheckUIDRequired)
	if err != nil {
		return err
	}

	if checkUID != nil {
		if err := s.validateCheckOnPage(ctx, page.UID, *checkUID); err != nil {
			return err
		}
	}

	update.CheckUID = checkUID

	return nil
}

// applySectionAndCheck handles the presence-aware sectionUid/checkUid fields
// on UpdateStatusUpdate. Clearing one never implicitly clears the other — the
// client decides each independently. The owning status page is fetched at
// most once, only when a non-empty value needs validating against it.
func (s *Service) applySectionAndCheck(
	ctx context.Context, orgUID string, update *models.StatusUpdate, req *UpdateStatusUpdateRequest,
) error {
	if !req.SectionUIDSet && !req.CheckUIDSet {
		return nil
	}

	needsPage := (req.SectionUIDSet && req.SectionUID != nil && *req.SectionUID != "") ||
		(req.CheckUIDSet && req.CheckUID != nil && *req.CheckUID != "")

	var page *models.StatusPage

	if needsPage {
		loaded, err := s.loadPageForUpdateValidation(ctx, orgUID, update)
		if err != nil {
			return err
		}

		page = loaded
	}

	if req.SectionUIDSet {
		if err := s.applySection(ctx, page, update, req); err != nil {
			return err
		}
	}

	if req.CheckUIDSet {
		if err := s.applyCheck(ctx, page, update, req); err != nil {
			return err
		}
	}

	return nil
}

// UpdateStatusUpdate applies a partial update to a status update.
//
//nolint:funlen // patching optional fields requires many checks
func (s *Service) UpdateStatusUpdate( //nolint:cyclop // patching optional fields requires checking each
	ctx context.Context, orgSlug, uid, actorUID string, req *UpdateStatusUpdateRequest,
) (StatusUpdateResponse, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return StatusUpdateResponse{}, ErrOrganizationNotFound
	}

	update, err := s.db.GetStatusUpdateByUID(ctx, uid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return StatusUpdateResponse{}, ErrStatusUpdateNotFound
		}

		return StatusUpdateResponse{}, err
	}

	if update.OrganizationUID != org.UID {
		return StatusUpdateResponse{}, ErrStatusUpdateNotFound
	}

	if req.Kind != nil {
		kind := models.StatusUpdateKind(*req.Kind)
		if !kind.IsValid() {
			return StatusUpdateResponse{}, ErrInvalidKind
		}

		update.Kind = kind
	}

	if req.Title != nil {
		if err := validateTitle(*req.Title); err != nil {
			return StatusUpdateResponse{}, err
		}

		update.Title = *req.Title
	}

	if req.BodyMarkdown != nil {
		if err := validateBody(*req.BodyMarkdown); err != nil {
			return StatusUpdateResponse{}, err
		}

		update.BodyMarkdown = *req.BodyMarkdown
	}

	if req.LinkURLSet {
		update.LinkURL = clearIfEmpty(req.LinkURL)

		if err := validateLinkURL(update.LinkURL); err != nil {
			return StatusUpdateResponse{}, err
		}
	}

	if err := s.applySectionAndCheck(ctx, org.UID, update, req); err != nil {
		return StatusUpdateResponse{}, err
	}

	if req.IncidentUIDSet {
		incidentUID, iErr := requireNonEmptyUID(req.IncidentUID, ErrIncidentUIDRequired)
		if iErr != nil {
			return StatusUpdateResponse{}, iErr
		}

		update.IncidentUID = incidentUID
	}

	if req.PublishedAt != nil {
		update.PublishedAt = *req.PublishedAt
	}

	if err := s.db.UpdateStatusUpdate(ctx, update); err != nil {
		return StatusUpdateResponse{}, err
	}

	event := models.NewEvent(org.UID, models.EventTypeStatusUpdateUpdated, models.ActorTypeUser)
	event.ActorUID = &actorUID
	event.Payload["status_update_uid"] = update.UID
	_ = s.db.CreateEvent(ctx, event)

	return toResponse(update), nil
}

// DeleteStatusUpdate soft-deletes a status update.
func (s *Service) DeleteStatusUpdate(
	ctx context.Context, orgSlug, uid, actorUID string,
) error {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return ErrOrganizationNotFound
	}

	update, err := s.db.GetStatusUpdateByUID(ctx, uid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrStatusUpdateNotFound
		}

		return err
	}

	if update.OrganizationUID != org.UID {
		return ErrStatusUpdateNotFound
	}

	if err := s.db.SoftDeleteStatusUpdate(ctx, uid); err != nil {
		return err
	}

	event := models.NewEvent(org.UID, models.EventTypeStatusUpdateDeleted, models.ActorTypeUser)
	event.ActorUID = &actorUID
	event.Payload["status_update_uid"] = uid
	_ = s.db.CreateEvent(ctx, event)

	return nil
}

// validateCheckOnPage checks that a check UID is registered as a resource on the given status page.
func (s *Service) validateCheckOnPage(ctx context.Context, pageUID, checkUID string) error {
	sections, err := s.db.ListStatusPageSections(ctx, pageUID)
	if err != nil {
		return err
	}

	for _, section := range sections {
		resources, resErr := s.db.ListStatusPageResources(ctx, section.UID)
		if resErr != nil {
			continue
		}

		for _, resource := range resources {
			// Group-targeting resources have a nil CheckUID (spec
			// 2026-08-01-03); a status update still pins to an individual check,
			// so only check resources can satisfy the "on this page" test.
			if resource.CheckUID != nil && *resource.CheckUID == checkUID {
				return nil
			}
		}
	}

	return ErrCheckNotFound
}
