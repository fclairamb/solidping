package incidentpublications

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/statusupdates"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/realtime"
	"github.com/fclairamb/solidping/server/internal/statuspagelock"
	clockpkg "github.com/fclairamb/solidping/server/internal/utils/clock"
)

// Service errors.
var (
	// ErrOrganizationNotFound is returned when the organization does not exist.
	ErrOrganizationNotFound = errors.New("organization not found")
	// ErrStatusPageNotFound is returned when the status page does not exist in the org.
	ErrStatusPageNotFound = errors.New("status page not found")
	// ErrPublicationNotFound is returned when the publication does not exist.
	ErrPublicationNotFound = errors.New("incident publication not found")
	// ErrIncidentNotFound is returned when the referenced incident does not exist.
	ErrIncidentNotFound = errors.New("incident not found")
	// ErrTitleRequired is returned when the public title is empty.
	ErrTitleRequired = errors.New("title is required")
	// ErrTitleTooLong is returned when the public title exceeds the limit.
	ErrTitleTooLong = errors.New("title must be at most 200 characters")
	// ErrInvalidState is returned when the public state is not recognized.
	ErrInvalidState = errors.New("invalid publication state")
	// ErrInvalidSeverity is returned when the severity is not recognized.
	ErrInvalidSeverity = errors.New("invalid publication severity")
	// ErrInvalidKind is returned when an update kind is not recognized.
	ErrInvalidKind = errors.New("invalid status update kind")
	// ErrBodyRequired is returned when an update body is empty.
	ErrBodyRequired = errors.New("body is required")
	// ErrBodyTooLong is returned when an update body exceeds the limit.
	ErrBodyTooLong = errors.New("body must be at most 16384 characters")
	// ErrUpdateWriteFailed is returned when the narrative row could not be
	// persisted, so the caller cannot be told the update was posted.
	ErrUpdateWriteFailed = errors.New("failed to append publication update")
	// ErrAlreadyPublished is returned when the incident is already published on
	// that page. It is a CONFLICT, not a silent no-op: the caller asked for a
	// second public incident and there can only be one.
	ErrAlreadyPublished = errors.New("incident is already published on this status page")
)

const (
	maxTitleLen = 200
	maxBodyLen  = 16384
	// notifyWindow is the rolling window the per-publication subscriber storm
	// cap counts sends in.
	notifyWindow = time.Hour
	// notifyCapParameterKey is the org parameter overriding
	// models.DefaultPublicationNotifyCap.
	notifyCapParameterKey = "status_page.publication_notify_cap"
)

// Service owns the publication overlay: manual CRUD, the auto-publish
// pipeline, and the public projection consumed by the status page.
type Service struct {
	db    db.Service
	clock clockpkg.Clock
	// rt publishes the org-scoped live hint so an open status page refreshes
	// when a publication changes. Nil-safe.
	rt *realtime.Publisher
	// notifier fans auto-generated updates out to confirmed subscribers. It is
	// the SAME notifier the hand-written status-update path uses, so email
	// double-opt-in, per-incident scoping and the Atom feed need no new
	// delivery code. Optional: nil disables fan-out.
	notifier statusupdates.SubscriberNotifier
	// jobs delivers publication webhooks. Optional: nil records the event row
	// but sends nothing outbound.
	jobs jobsvc.Service
	// scheduler enqueues the debounce job. Optional: nil publishes inline,
	// which is what unit tests and a zero delay both want.
	scheduler PublishScheduler
	logger    *slog.Logger
}

// PublishScheduler enqueues the debounced auto-publish job. It is an interface
// so this package does not import the job registry (which imports this
// package to run the job).
type PublishScheduler interface {
	// SchedulePublish asks for AutoPublish(incidentUID, statusPageUID) to be
	// run at `at`. Implementations must tolerate duplicates — the publication
	// itself is idempotent.
	SchedulePublish(ctx context.Context, orgUID, incidentUID, statusPageUID string, fireAt time.Time) error
}

// NewService builds the publication service. clk may be nil (real clock);
// rt may be nil (realtime disabled).
func NewService(dbService db.Service, clk clockpkg.Clock, rt *realtime.Publisher) *Service {
	if clk == nil {
		clk = clockpkg.Real{}
	}

	return &Service{db: dbService, clock: clk, rt: rt, logger: slog.Default()}
}

// SetSubscriberNotifier wires the subscriber fan-out. Optional.
func (s *Service) SetSubscriberNotifier(n statusupdates.SubscriberNotifier) {
	s.notifier = n
}

// SetScheduler wires the debounce-job scheduler. Optional — without it a
// delayed publication is performed inline at trigger time, which is wrong for
// production (it defeats the debounce) and is why server.go always wires one.
func (s *Service) SetScheduler(sched PublishScheduler) {
	s.scheduler = sched
}

// SetLogger overrides the default logger.
func (s *Service) SetLogger(l *slog.Logger) {
	if l != nil {
		s.logger = l
	}
}

// --- Wire types ---

// PublicationResponse is the operator-facing shape of a publication.
type PublicationResponse struct {
	UID           string     `json:"uid"`
	StatusPageUID string     `json:"statusPageUid"`
	IncidentUID   *string    `json:"incidentUid,omitempty"`
	Title         string     `json:"title"`
	State         string     `json:"state"`
	Severity      *string    `json:"severity,omitempty"`
	AutoCreated   bool       `json:"autoCreated"`
	HumanTouched  bool       `json:"humanTouched"`
	PublishedAt   time.Time  `json:"publishedAt"`
	ResolvedAt    *time.Time `json:"resolvedAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	// Updates is the append-only narrative, oldest first. Populated on the
	// single-publication reads and on the public projection.
	Updates []PublicationUpdateResponse `json:"updates,omitempty"`
	// AffectedResources are the PUBLIC display names of the page resources this
	// publication covers. Derived from the page's own resources, so it can only
	// ever contain names the page already shows.
	AffectedResources []string `json:"affectedResources,omitempty"`
}

// PublicationUpdateResponse is one narrative entry.
type PublicationUpdateResponse struct {
	UID          string    `json:"uid"`
	Kind         string    `json:"kind"`
	Title        string    `json:"title"`
	BodyMarkdown string    `json:"bodyMarkdown"`
	PublishedAt  time.Time `json:"publishedAt"`
	// AuthorUID is nil for machine-generated updates.
	AuthorUID *string `json:"authorUid,omitempty"`
}

// CreatePublicationRequest is the manual-create payload.
type CreatePublicationRequest struct {
	Title    string  `json:"title"`
	State    *string `json:"state,omitempty"`
	Severity *string `json:"severity,omitempty"`
	// IncidentUID optionally binds the publication to an operational incident.
	IncidentUID *string `json:"incidentUid,omitempty"`
	// BodyMarkdown is the optional first narrative entry.
	BodyMarkdown *string `json:"bodyMarkdown,omitempty"`
}

// UpdatePublicationRequest patches a publication. Any field being present
// stamps human_touched_at.
type UpdatePublicationRequest struct {
	Title    *string `json:"title,omitempty"`
	State    *string `json:"state,omitempty"`
	Severity *string `json:"severity,omitempty"`
}

// AppendUpdateRequest appends one narrative entry. Updates are append-only:
// there is deliberately no edit or delete endpoint for them.
type AppendUpdateRequest struct {
	Kind         string  `json:"kind"`
	Title        *string `json:"title,omitempty"`
	BodyMarkdown string  `json:"bodyMarkdown"`
}

// PublishIncidentRequest publishes an existing incident onto a page.
type PublishIncidentRequest struct {
	StatusPageUID string  `json:"statusPageUid"`
	Title         *string `json:"title,omitempty"`
	Severity      *string `json:"severity,omitempty"`
}

// ListOptions narrows a publication listing.
type ListOptions struct {
	State      string
	ActiveOnly bool
	Limit      int
	Offset     int
}

func toResponse(pub *models.IncidentPublication) PublicationResponse {
	return PublicationResponse{
		UID:           pub.UID,
		StatusPageUID: pub.StatusPageUID,
		IncidentUID:   pub.IncidentUID,
		Title:         pub.PublicTitle,
		State:         string(pub.PublicState),
		Severity:      pub.Severity,
		AutoCreated:   pub.AutoCreated,
		HumanTouched:  pub.HumanTouchedAt != nil,
		PublishedAt:   pub.PublishedAt,
		ResolvedAt:    pub.ResolvedAt,
		CreatedAt:     pub.CreatedAt,
		UpdatedAt:     pub.UpdatedAt,
	}
}

// --- Validation ---

func validateTitle(title string) error {
	if strings.TrimSpace(title) == "" {
		return ErrTitleRequired
	}

	if len(title) > maxTitleLen {
		return ErrTitleTooLong
	}

	return nil
}

func validateSeverity(sev *string) error {
	if sev == nil || *sev == "" {
		return nil
	}

	if !models.PublicationSeverity(*sev).IsValid() {
		return ErrInvalidSeverity
	}

	return nil
}

func validateBody(body string) error {
	if strings.TrimSpace(body) == "" {
		return ErrBodyRequired
	}

	if len(body) > maxBodyLen {
		return ErrBodyTooLong
	}

	return nil
}

// --- Resolution helpers ---

func (s *Service) resolveOrg(ctx context.Context, orgSlug string) (*models.Organization, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	return org, nil
}

// resolvePage accepts a page UID or slug, exactly like the status-page
// endpoints do.
func (s *Service) resolvePage(
	ctx context.Context, orgUID, identifier string,
) (*models.StatusPage, error) {
	page, err := s.db.GetStatusPage(ctx, orgUID, identifier)
	if err == nil && page != nil {
		return page, nil
	}

	page, err = s.db.GetStatusPageBySlug(ctx, orgUID, identifier)
	if err != nil || page == nil {
		return nil, ErrStatusPageNotFound
	}

	return page, nil
}

func (s *Service) getPublication(
	ctx context.Context, orgUID, pageUID, uid string,
) (*models.IncidentPublication, error) {
	pub, err := s.db.GetIncidentPublication(ctx, orgUID, uid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPublicationNotFound
		}

		return nil, err
	}

	if pageUID != "" && pub.StatusPageUID != pageUID {
		return nil, ErrPublicationNotFound
	}

	return pub, nil
}

// --- Manual CRUD ---

// ListPublications returns the publications of one status page.
func (s *Service) ListPublications(
	ctx context.Context, orgSlug, pageIdentifier string, opts ListOptions,
) ([]PublicationResponse, error) {
	org, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	page, err := s.resolvePage(ctx, org.UID, pageIdentifier)
	if err != nil {
		return nil, err
	}

	pubs, err := s.db.ListIncidentPublications(ctx, &models.ListIncidentPublicationsFilter{
		OrganizationUID: org.UID,
		StatusPageUID:   page.UID,
		State:           opts.State,
		ActiveOnly:      opts.ActiveOnly,
		Limit:           opts.Limit,
		Offset:          opts.Offset,
	})
	if err != nil {
		return nil, err
	}

	out := make([]PublicationResponse, len(pubs))
	for idx, pub := range pubs {
		out[idx] = toResponse(pub)
	}

	return out, nil
}

// GetPublication reads one publication with its narrative.
func (s *Service) GetPublication(
	ctx context.Context, orgSlug, pageIdentifier, uid string,
) (PublicationResponse, error) {
	org, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return PublicationResponse{}, err
	}

	page, err := s.resolvePage(ctx, org.UID, pageIdentifier)
	if err != nil {
		return PublicationResponse{}, err
	}

	pub, err := s.getPublication(ctx, org.UID, page.UID, uid)
	if err != nil {
		return PublicationResponse{}, err
	}

	resp := toResponse(pub)
	resp.Updates = s.loadUpdates(ctx, org.UID, pub)
	resp.AffectedResources = s.affectedResourceNames(ctx, org.UID, pub)

	return resp, nil
}

// CreatePublication creates a hand-authored publication. It is always
// human-touched from birth: a person wrote it, so no automation may ever
// resolve or rewrite it behind their back.
//
//nolint:cyclop // one branch per optional field; splitting only hides the shape.
func (s *Service) CreatePublication(
	ctx context.Context, orgSlug, pageIdentifier, actorUID string, req *CreatePublicationRequest,
) (PublicationResponse, error) {
	org, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return PublicationResponse{}, err
	}

	page, err := s.resolvePage(ctx, org.UID, pageIdentifier)
	if err != nil {
		return PublicationResponse{}, err
	}

	if err := validateTitle(req.Title); err != nil {
		return PublicationResponse{}, err
	}

	if err := validateSeverity(req.Severity); err != nil {
		return PublicationResponse{}, err
	}

	state := models.PublicationStateInvestigating
	if req.State != nil && *req.State != "" {
		state = models.PublicationState(*req.State)
		if !state.IsValid() {
			return PublicationResponse{}, ErrInvalidState
		}
	}

	if req.IncidentUID != nil && *req.IncidentUID != "" {
		if _, err := s.db.GetIncident(ctx, org.UID, *req.IncidentUID); err != nil {
			return PublicationResponse{}, ErrIncidentNotFound
		}

		if existing, findErr := s.db.FindIncidentPublication(ctx, *req.IncidentUID, page.UID); findErr == nil &&
			existing != nil {
			return PublicationResponse{}, ErrAlreadyPublished
		}
	}

	now := s.clock.Now()
	pub := models.NewIncidentPublication(org.UID, page.UID, req.Title, now)
	pub.PublicState = state
	pub.Severity = req.Severity
	pub.HumanTouchedAt = &now

	if req.IncidentUID != nil && *req.IncidentUID != "" {
		pub.IncidentUID = req.IncidentUID
	}

	if err := s.db.CreateIncidentPublication(ctx, pub); err != nil {
		return PublicationResponse{}, err
	}

	if req.BodyMarkdown != nil && strings.TrimSpace(*req.BodyMarkdown) != "" {
		if err := validateBody(*req.BodyMarkdown); err != nil {
			return PublicationResponse{}, err
		}

		s.postUpdate(ctx, page, pub, state.UpdateKind(), req.Title, *req.BodyMarkdown, &actorUID)
	}

	s.emit(ctx, org.UID, models.EventTypeStatusPageIncidentPublished, pub, actorUID)
	s.publishHint(ctx, org.UID)

	return toResponse(pub), nil
}

// UpdatePublication patches title / state / severity. ANY edit stamps
// human_touched_at — that stamp is the whole basis of the `if_untouched`
// auto-resolve policy, so it is set here and never anywhere in the automation.
func (s *Service) UpdatePublication(
	ctx context.Context, orgSlug, pageIdentifier, uid, actorUID string, req *UpdatePublicationRequest,
) (PublicationResponse, error) {
	org, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return PublicationResponse{}, err
	}

	page, err := s.resolvePage(ctx, org.UID, pageIdentifier)
	if err != nil {
		return PublicationResponse{}, err
	}

	pub, err := s.getPublication(ctx, org.UID, page.UID, uid)
	if err != nil {
		return PublicationResponse{}, err
	}

	now := s.clock.Now()
	update := &models.IncidentPublicationUpdate{HumanTouchedAt: &now}

	if req.Title != nil {
		if err := validateTitle(*req.Title); err != nil {
			return PublicationResponse{}, err
		}

		update.PublicTitle = req.Title
		pub.PublicTitle = *req.Title
	}

	if req.Severity != nil {
		if err := validateSeverity(req.Severity); err != nil {
			return PublicationResponse{}, err
		}

		if *req.Severity == "" {
			update.ClearSeverity = true
			pub.Severity = nil
		} else {
			update.Severity = req.Severity
			pub.Severity = req.Severity
		}
	}

	if req.State != nil {
		state := models.PublicationState(*req.State)
		if !state.IsValid() {
			return PublicationResponse{}, ErrInvalidState
		}

		update.PublicState = &state
		pub.PublicState = state

		if state == models.PublicationStateResolved {
			update.ResolvedAt = &now
			pub.ResolvedAt = &now
		} else {
			update.ClearResolvedAt = true
			pub.ResolvedAt = nil
		}
	}

	if err := s.db.UpdateIncidentPublication(ctx, pub.UID, update); err != nil {
		return PublicationResponse{}, err
	}

	pub.HumanTouchedAt = &now

	eventType := models.EventTypeStatusPageIncidentUpdated
	if pub.PublicState == models.PublicationStateResolved {
		eventType = models.EventTypeStatusPageIncidentResolved
	}

	s.emit(ctx, org.UID, eventType, pub, actorUID)
	s.publishHint(ctx, org.UID)

	return toResponse(pub), nil
}

// AppendUpdate appends one narrative entry and fans it out to subscribers.
// Updates are append-only by design (matching the status_updates semantics the
// public timeline already has): a posted update is a promise, not a draft.
func (s *Service) AppendUpdate(
	ctx context.Context, orgSlug, pageIdentifier, uid, actorUID string, req *AppendUpdateRequest,
) (PublicationUpdateResponse, error) {
	org, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return PublicationUpdateResponse{}, err
	}

	page, err := s.resolvePage(ctx, org.UID, pageIdentifier)
	if err != nil {
		return PublicationUpdateResponse{}, err
	}

	pub, err := s.getPublication(ctx, org.UID, page.UID, uid)
	if err != nil {
		return PublicationUpdateResponse{}, err
	}

	kind := models.StatusUpdateKind(req.Kind)
	if !kind.IsValid() {
		return PublicationUpdateResponse{}, ErrInvalidKind
	}

	if err := validateBody(req.BodyMarkdown); err != nil {
		return PublicationUpdateResponse{}, err
	}

	title := pub.PublicTitle
	if req.Title != nil && *req.Title != "" {
		if err := validateTitle(*req.Title); err != nil {
			return PublicationUpdateResponse{}, err
		}

		title = *req.Title
	}

	now := s.clock.Now()
	stateUpdate := &models.IncidentPublicationUpdate{HumanTouchedAt: &now}

	// An update whose kind maps onto a publication state advances the state
	// too, so the header and the timeline can never disagree.
	if state := stateForKind(kind); state != "" {
		stateUpdate.PublicState = &state
		pub.PublicState = state

		if state == models.PublicationStateResolved {
			stateUpdate.ResolvedAt = &now
			pub.ResolvedAt = &now
		}
	}

	if err := s.db.UpdateIncidentPublication(ctx, pub.UID, stateUpdate); err != nil {
		return PublicationUpdateResponse{}, err
	}

	pub.HumanTouchedAt = &now

	written := s.postUpdate(ctx, page, pub, kind, title, req.BodyMarkdown, &actorUID)
	if written == nil {
		return PublicationUpdateResponse{}, ErrUpdateWriteFailed
	}

	eventType := models.EventTypeStatusPageIncidentUpdated
	if pub.PublicState == models.PublicationStateResolved {
		eventType = models.EventTypeStatusPageIncidentResolved
	}

	s.emit(ctx, org.UID, eventType, pub, actorUID)
	s.publishHint(ctx, org.UID)

	return PublicationUpdateResponse{
		UID:          written.UID,
		Kind:         string(written.Kind),
		Title:        written.Title,
		BodyMarkdown: written.BodyMarkdown,
		PublishedAt:  written.PublishedAt,
		AuthorUID:    written.AuthorUID,
	}, nil
}

// stateForKind maps an update kind onto the publication state it implies.
// `maintenance` and `info` are narrative-only and leave the state alone.
func stateForKind(kind models.StatusUpdateKind) models.PublicationState {
	switch kind {
	case models.StatusUpdateKindInvestigating:
		return models.PublicationStateInvestigating
	case models.StatusUpdateKindIdentified:
		return models.PublicationStateIdentified
	case models.StatusUpdateKindMonitoring:
		return models.PublicationStateMonitoring
	case models.StatusUpdateKindResolved:
		return models.PublicationStateResolved
	case models.StatusUpdateKindMaintenance, models.StatusUpdateKindInfo:
		return ""
	default:
		return ""
	}
}

// --- Incident-side publish / unpublish ---

// PublishIncident publishes an existing incident onto a status page by hand.
func (s *Service) PublishIncident(
	ctx context.Context, orgSlug, incidentUID, actorUID string, req *PublishIncidentRequest,
) (PublicationResponse, error) {
	org, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return PublicationResponse{}, err
	}

	incident, err := s.db.GetIncident(ctx, org.UID, incidentUID)
	if err != nil || incident == nil {
		return PublicationResponse{}, ErrIncidentNotFound
	}

	page, err := s.resolvePage(ctx, org.UID, req.StatusPageUID)
	if err != nil {
		return PublicationResponse{}, err
	}

	if err := validateSeverity(req.Severity); err != nil {
		return PublicationResponse{}, err
	}

	if existing, findErr := s.db.FindIncidentPublication(ctx, incident.UID, page.UID); findErr == nil &&
		existing != nil {
		return PublicationResponse{}, ErrAlreadyPublished
	}

	title := ""
	if req.Title != nil {
		title = *req.Title
	}

	if title == "" {
		// Never fall back to incident.Title: that string is generated from the
		// check SLUG and is internal vocabulary. Template from the page's own
		// public resource name instead.
		title = s.templatedTitle(ctx, org.UID, page, incident)
	}

	if err := validateTitle(title); err != nil {
		return PublicationResponse{}, err
	}

	now := s.clock.Now()
	pub := models.NewIncidentPublication(org.UID, page.UID, title, now)
	pub.IncidentUID = &incident.UID
	pub.Severity = req.Severity
	pub.HumanTouchedAt = &now

	if err := s.db.CreateIncidentPublication(ctx, pub); err != nil {
		if isUniqueViolation(err) {
			return PublicationResponse{}, ErrAlreadyPublished
		}

		return PublicationResponse{}, err
	}

	tpl := templatesFor(page.Language)
	name := s.affectedName(ctx, org.UID, page, incident)
	s.postUpdate(ctx, page, pub, models.StatusUpdateKindInvestigating,
		title, fmt.Sprintf(tpl.OpenedBody, name), &actorUID)

	s.emit(ctx, org.UID, models.EventTypeStatusPageIncidentPublished, pub, actorUID)
	s.publishHint(ctx, org.UID)

	return toResponse(pub), nil
}

// ListForIncident returns every live publication of one incident, across pages.
func (s *Service) ListForIncident(
	ctx context.Context, orgSlug, incidentUID string,
) ([]PublicationResponse, error) {
	org, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	pubs, err := s.db.ListIncidentPublications(ctx, &models.ListIncidentPublicationsFilter{
		OrganizationUID: org.UID,
		IncidentUID:     incidentUID,
	})
	if err != nil {
		return nil, err
	}

	out := make([]PublicationResponse, len(pubs))
	for idx, pub := range pubs {
		out[idx] = toResponse(pub)
	}

	return out, nil
}

// UnpublishIncident removes a publication from its page. The row is
// soft-deleted, which also frees the partial unique index so the same incident
// can be republished later.
func (s *Service) UnpublishIncident(
	ctx context.Context, orgSlug, incidentUID, publicationUID, actorUID string,
) error {
	org, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return err
	}

	pub, err := s.getPublication(ctx, org.UID, "", publicationUID)
	if err != nil {
		return err
	}

	if pub.IncidentUID == nil || *pub.IncidentUID != incidentUID {
		return ErrPublicationNotFound
	}

	if err := s.db.SoftDeleteIncidentPublication(ctx, pub.UID); err != nil {
		return err
	}

	s.emit(ctx, org.UID, models.EventTypeStatusPageIncidentResolved, pub, actorUID)
	s.publishHint(ctx, org.UID)

	return nil
}

// --- Shared helpers ---

// loadUpdates returns the publication's narrative, oldest first.
func (s *Service) loadUpdates(
	ctx context.Context, orgUID string, pub *models.IncidentPublication,
) []PublicationUpdateResponse {
	updates, err := s.db.ListStatusUpdates(ctx, orgUID, models.StatusUpdatesFilter{
		StatusPageUID:          pub.StatusPageUID,
		IncidentPublicationUID: &pub.UID,
		Limit:                  200,
	})
	if err != nil {
		s.logger.WarnContext(ctx, "failed to load publication updates",
			"publicationUid", pub.UID, "error", err)

		return nil
	}

	out := make([]PublicationUpdateResponse, 0, len(updates))
	// ListStatusUpdates orders published_at DESC; the narrative reads oldest
	// first.
	for idx := len(updates) - 1; idx >= 0; idx-- {
		upd := updates[idx]
		out = append(out, PublicationUpdateResponse{
			UID:          upd.UID,
			Kind:         string(upd.Kind),
			Title:        upd.Title,
			BodyMarkdown: upd.BodyMarkdown,
			PublishedAt:  upd.PublishedAt,
			AuthorUID:    upd.AuthorUID,
		})
	}

	return out
}

// postUpdate writes one status_updates row for the publication and fans it out
// to subscribers under the storm cap. Returns the written row, or nil on
// failure — a failed narrative post is logged and never fails the caller,
// because losing an update is better than losing the state transition.
func (s *Service) postUpdate(
	ctx context.Context, page *models.StatusPage, pub *models.IncidentPublication,
	kind models.StatusUpdateKind, title, body string, authorUID *string,
) *models.StatusUpdate {
	author := ""
	if authorUID != nil {
		author = *authorUID
	}

	update := models.NewStatusUpdate(pub.OrganizationUID, page.UID, author)
	update.Kind = kind
	update.Title = title
	update.BodyMarkdown = body
	update.IncidentUID = pub.IncidentUID
	update.IncidentPublicationUID = &pub.UID
	update.PublishedAt = s.clock.Now()
	update.CreatedAt = update.PublishedAt
	update.UpdatedAt = update.PublishedAt

	if err := s.db.CreateStatusUpdate(ctx, update); err != nil {
		s.logger.ErrorContext(ctx, "failed to write publication update",
			"publicationUid", pub.UID, "error", err)

		return nil
	}

	s.fanOut(ctx, page, pub, update)

	return update
}

// fanOut delivers one update to confirmed subscribers, under the
// per-publication storm cap. The cap protects a flapping group incident from
// mailing every subscriber a dozen times an hour; the update itself is always
// written, so the public page and the feed stay complete even when the mail
// stops.
func (s *Service) fanOut(
	ctx context.Context, page *models.StatusPage,
	pub *models.IncidentPublication, update *models.StatusUpdate,
) {
	if s.notifier == nil {
		return
	}

	if !s.consumeNotifyBudget(ctx, pub) {
		s.logger.InfoContext(ctx, "publication storm cap reached, update posted without subscriber mail",
			"publicationUid", pub.UID)

		return
	}

	priorCount := 0

	existing, err := s.db.ListStatusUpdates(ctx, pub.OrganizationUID, models.StatusUpdatesFilter{
		StatusPageUID:          page.UID,
		IncidentPublicationUID: &pub.UID,
		Limit:                  200,
	})
	if err == nil {
		priorCount = len(existing) - 1
		if priorCount < 0 {
			priorCount = 0
		}
	}

	event := &statusupdates.SubscriberUpdateEvent{
		StatusPageUID:       page.UID,
		IncidentUID:         pub.IncidentUID,
		Kind:                string(update.Kind),
		Title:               update.Title,
		BodyMarkdown:        update.BodyMarkdown,
		LinkURL:             update.LinkURL,
		PageName:            page.Name,
		IncidentUpdateCount: priorCount,
	}

	bgCtx := context.WithoutCancel(ctx)

	go s.notifier.NotifyStatusUpdate(bgCtx, event)
}

// consumeNotifyBudget reserves one subscriber send inside the publication's
// rolling hour. Returns false when the cap is already spent.
func (s *Service) consumeNotifyBudget(ctx context.Context, pub *models.IncidentPublication) bool {
	budget := s.notifyCap(ctx, pub.OrganizationUID)
	now := s.clock.Now()

	windowStart := pub.NotifyWindowStart
	count := pub.NotifyWindowCount

	if windowStart == nil || now.Sub(*windowStart) >= notifyWindow {
		windowStart = &now
		count = 0
	}

	if count >= budget {
		return false
	}

	count++

	if err := s.db.UpdateIncidentPublication(ctx, pub.UID, &models.IncidentPublicationUpdate{
		NotifyWindowStart: windowStart,
		NotifyWindowCount: &count,
	}); err != nil {
		s.logger.WarnContext(ctx, "failed to persist publication notify budget",
			"publicationUid", pub.UID, "error", err)
	}

	pub.NotifyWindowStart = windowStart
	pub.NotifyWindowCount = count

	return true
}

// notifyCap resolves the per-hour subscriber send cap for an org.
func (s *Service) notifyCap(ctx context.Context, orgUID string) int {
	param, err := s.db.GetOrgParameter(ctx, orgUID, notifyCapParameterKey)
	if err != nil || param == nil {
		return models.DefaultPublicationNotifyCap
	}

	raw, ok := param.Value[models.ParameterValueKey]
	if !ok {
		return models.DefaultPublicationNotifyCap
	}

	switch value := raw.(type) {
	case float64:
		if value >= 0 {
			return int(value)
		}
	case int:
		if value >= 0 {
			return value
		}
	case int64:
		if value >= 0 {
			return int(value)
		}
	}

	return models.DefaultPublicationNotifyCap
}

// publishHint nudges open dashboards and status pages to refetch.
func (s *Service) publishHint(ctx context.Context, orgUID string) {
	s.rt.PublishImmediate(ctx, orgUID, "", realtime.KindIncidents)
}

// emit records the publication-lifecycle event row. These are DISTINCT from
// the internal incident.* events: an operational incident opening and a
// customer-visible incident being published are different facts, and a webhook
// consumer must be able to subscribe to one without the other.
func (s *Service) emit(
	ctx context.Context, orgUID string, eventType models.EventType,
	pub *models.IncidentPublication, actorUID string,
) {
	actorType := models.ActorTypeSystem
	if actorUID != "" {
		actorType = models.ActorTypeUser
	}

	event := models.NewEvent(orgUID, eventType, actorType)
	if actorUID != "" {
		event.ActorUID = &actorUID
	}

	event.Payload["publication_uid"] = pub.UID
	event.Payload["status_page_uid"] = pub.StatusPageUID
	event.Payload["public_state"] = string(pub.PublicState)
	event.Payload["public_title"] = pub.PublicTitle

	if pub.IncidentUID != nil {
		event.Payload["incident_uid"] = *pub.IncidentUID
	}

	if err := s.db.CreateEvent(ctx, event); err != nil {
		s.logger.WarnContext(ctx, "failed to record publication event",
			"publicationUid", pub.UID, "error", err)
	}

	s.dispatchWebhooks(ctx, orgUID, eventType, pub)
}

// isUniqueViolation reports whether the error is a duplicate-key violation on
// either dialect. String matching, like the incidents package's sibling helper:
// the drivers wrap errors differently and the caller always recovers by
// re-reading.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()

	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "duplicate key value") ||
		strings.Contains(msg, "SQLSTATE 23505")
}

// --- Public projection ---

// PublicIncident is the customer-facing shape of a publication. Every field is
// either operator-authored or templated from the page's own public resource
// names; there is deliberately no field a probe diagnostic could occupy.
type PublicIncident struct {
	UID               string                 `json:"uid"`
	Title             string                 `json:"title"`
	State             string                 `json:"state"`
	Severity          *string                `json:"severity,omitempty"`
	StartedAt         time.Time              `json:"startedAt"`
	ResolvedAt        *time.Time             `json:"resolvedAt,omitempty"`
	AffectedResources []string               `json:"affectedResources,omitempty"`
	Updates           []PublicIncidentUpdate `json:"updates,omitempty"`
}

// PublicIncidentUpdate is one narrative entry on the public page.
type PublicIncidentUpdate struct {
	UID          string    `json:"uid"`
	Kind         string    `json:"kind"`
	Title        string    `json:"title"`
	BodyMarkdown string    `json:"bodyMarkdown"`
	PublishedAt  time.Time `json:"publishedAt"`
}

// ListPublicIncidents returns the publications to render on a status page.
// activeOnly=true is the banner/active-section feed; false returns the history
// within the page's own history window.
func (s *Service) ListPublicIncidents(
	ctx context.Context, page *models.StatusPage, activeOnly bool,
) ([]PublicIncident, error) {
	filter := &models.ListIncidentPublicationsFilter{
		OrganizationUID: page.OrganizationUID,
		StatusPageUID:   page.UID,
		ActiveOnly:      activeOnly,
		Limit:           100,
	}

	pubs, err := s.db.ListIncidentPublications(ctx, filter)
	if err != nil {
		return nil, err
	}

	cutoff := time.Time{}
	if !activeOnly && page.HistoryDays > 0 {
		cutoff = s.clock.Now().AddDate(0, 0, -page.HistoryDays)
	}

	out := make([]PublicIncident, 0, len(pubs))

	for _, pub := range pubs {
		if !cutoff.IsZero() && pub.PublishedAt.Before(cutoff) {
			continue
		}

		entry := PublicIncident{
			UID:               pub.UID,
			Title:             pub.PublicTitle,
			State:             string(pub.PublicState),
			Severity:          pub.Severity,
			StartedAt:         pub.PublishedAt,
			ResolvedAt:        pub.ResolvedAt,
			AffectedResources: s.affectedResourceNames(ctx, page.OrganizationUID, pub),
		}

		updates := s.loadUpdates(ctx, page.OrganizationUID, pub)
		for idx := range updates {
			entry.Updates = append(entry.Updates, PublicIncidentUpdate{
				UID:          updates[idx].UID,
				Kind:         updates[idx].Kind,
				Title:        updates[idx].Title,
				BodyMarkdown: updates[idx].BodyMarkdown,
				PublishedAt:  updates[idx].PublishedAt,
			})
		}

		out = append(out, entry)
	}

	return out, nil
}

// affectedResourceNames resolves the PUBLIC names of the page resources a
// publication covers. It walks the page's own resources, so the result can only
// ever contain names the page already renders — a check whose resource has no
// public_name contributes its check NAME (never its slug, never its target
// host), exactly the same fallback the resource list itself uses.
func (s *Service) affectedResourceNames(
	ctx context.Context, orgUID string, pub *models.IncidentPublication,
) []string {
	if pub.IncidentUID == nil {
		return nil
	}

	incident, err := s.db.GetIncident(ctx, orgUID, *pub.IncidentUID)
	if err != nil || incident == nil {
		return nil
	}

	checkUIDs := []string{incident.CheckUID}

	if incident.CheckGroupUID != nil {
		members, memErr := s.db.ListIncidentMemberChecks(ctx, incident.UID)
		if memErr == nil {
			for _, member := range members {
				checkUIDs = append(checkUIDs, member.CheckUID)
			}
		}
	}

	seen := make(map[string]struct{}, len(checkUIDs))
	names := make([]string, 0, len(checkUIDs))

	for _, checkUID := range checkUIDs {
		if checkUID == "" {
			continue
		}

		for _, name := range s.resourceNamesOnPage(ctx, orgUID, pub.StatusPageUID, checkUID, incident.CheckGroupUID) {
			if _, dup := seen[name]; dup {
				continue
			}

			seen[name] = struct{}{}
			names = append(names, name)
		}
	}

	return names
}

// resourceNamesOnPage returns the public display names under which a check
// appears on one page.
func (s *Service) resourceNamesOnPage(
	ctx context.Context, orgUID, pageUID, checkUID string, groupUID *string,
) []string {
	targets, err := s.db.ListStatusPageTargetsForCheck(ctx, checkUID, groupUID)
	if err != nil {
		return nil
	}

	var names []string

	for _, target := range targets {
		if target.PageUID != pageUID {
			continue
		}

		if target.ResourcePublicName != nil && *target.ResourcePublicName != "" {
			names = append(names, *target.ResourcePublicName)

			continue
		}

		if target.ViaGroup {
			if groupUID != nil {
				if group, gErr := s.db.GetCheckGroup(ctx, orgUID, *groupUID); gErr == nil && group != nil {
					names = append(names, group.Name)
				}
			}

			continue
		}

		if check, cErr := s.db.GetCheck(ctx, orgUID, checkUID); cErr == nil && check != nil &&
			check.Name != nil && *check.Name != "" {
			names = append(names, *check.Name)
		}
	}

	return names
}

// affectedName returns the single best public name for an incident on a page,
// used to build a templated title. Falls back to the empty string when the page
// shows the check only through an unnamed group.
func (s *Service) affectedName(
	ctx context.Context, orgUID string, page *models.StatusPage, incident *models.Incident,
) string {
	names := s.resourceNamesOnPage(ctx, orgUID, page.UID, incident.CheckUID, incident.CheckGroupUID)
	if len(names) > 0 {
		return names[0]
	}

	return ""
}

// templatedTitle builds the public title from the page's public resource name
// — never from incident.Title, which is generated from the check slug and is
// internal vocabulary.
func (s *Service) templatedTitle(
	ctx context.Context, orgUID string, page *models.StatusPage, incident *models.Incident,
) string {
	tpl := templatesFor(page.Language)

	name := s.affectedName(ctx, orgUID, page, incident)
	if name == "" {
		return tpl.GroupOpenedTitle
	}

	return fmt.Sprintf(tpl.OpenedTitle, name)
}

// ViewPublicIncidents is the PUBLIC projection entry point: it resolves the
// page by org+slug and applies the same visibility gate as every other public
// status-page endpoint, so a private or disabled page's incidents are as
// invisible as the page itself.
func (s *Service) ViewPublicIncidents(
	ctx context.Context, orgSlug, slug string, activeOnly bool,
) ([]PublicIncident, error) {
	org, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	page, err := s.db.GetStatusPageBySlug(ctx, org.UID, slug)
	if err != nil || page == nil {
		return nil, ErrStatusPageNotFound
	}

	// Identical gate to statuspages.ViewStatusPage: not enabled, or private,
	// is indistinguishable from not existing.
	if !statuspagelock.Visible(page) {
		return nil, ErrStatusPageNotFound
	}

	// A password page's incident history is part of what the password buys.
	if !statuspagelock.Allows(ctx, page) {
		return nil, statuspagelock.ErrLocked
	}

	return s.ListPublicIncidents(ctx, page, activeOnly)
}
