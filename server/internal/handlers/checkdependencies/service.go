// Package checkdependencies provides CRUD over the check_dependencies graph
// and the helpers used by the incident-rollup hook.
package checkdependencies

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Sentinel errors returned by the service. The handler maps them to error codes.
var (
	ErrOrganizationNotFound = errors.New("organization not found")
	ErrCheckNotFound        = errors.New("check not found")
	ErrDependencyNotFound   = errors.New("dependency not found")
	ErrSelfEdge             = errors.New("self-edge not allowed")
	ErrCrossOrg             = errors.New("cross-org edge not allowed")
	ErrCycle                = errors.New("dependency would create a cycle")
	ErrDuplicate            = errors.New("dependency already exists")
	ErrInvalidKind          = errors.New("invalid dependency kind")
)

// Service holds the dependency-graph business logic.
type Service struct {
	db db.Service

	// defaultCheckTimeout is the server's resolved
	// `scheduling.check_timeout_ms`. It is the fallback term in the
	// confirmation-margin lint below, for a parent that left `timeout` unset.
	defaultCheckTimeout time.Duration
}

// NewService creates a new dependency service. `defaultCheckTimeout` is the
// resolved `scheduling.check_timeout_ms`; a non-positive value falls back to
// the shipped default so a partially-wired caller still lints sensibly.
func NewService(dbService db.Service, defaultCheckTimeout time.Duration) *Service {
	if defaultCheckTimeout <= 0 {
		defaultCheckTimeout = DefaultCheckTimeoutFallback
	}

	return &Service{db: dbService, defaultCheckTimeout: defaultCheckTimeout}
}

// DefaultCheckTimeoutFallback mirrors the shipped default of
// `scheduling.check_timeout_ms` (15s).
const DefaultCheckTimeoutFallback = 15 * time.Second

// WarningCodeConfirmationMarginTooShort marks a hard `dependsOn` edge whose
// child can finish confirming before its parent could possibly have detected
// the same outage.
//
// SOFT — advisory only, never a validation error. The strict margin
// (`child.confirmation >= parent.confirmation + parent.period +
// parent.timeout`) is the right thing to aim for, but enforcing it would tax
// every child incident with the margin even when the parent is healthy, and
// would turn a single check edit into cross-resource validation. The runtime
// confirmation hold (spec 2026-08-31-06) already covers the gap at page time;
// this warning only explains why a page may arrive later than the child's
// configured confirmation suggests.
const WarningCodeConfirmationMarginTooShort = "CONFIRMATION_MARGIN_TOO_SHORT"

// CheckRef is a minimal {uid, slug, name} reference inlined into responses.
type CheckRef struct {
	UID  string `json:"uid"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// DependencyResponse is the standard API row for a single edge.
type DependencyResponse struct {
	UID         string   `json:"uid"`
	ParentCheck CheckRef `json:"parentCheck"`
	ChildCheck  CheckRef `json:"childCheck"`
	Kind        string   `json:"kind"`
	Description *string  `json:"description,omitempty"`
}

// PerCheckDependencies bundles parents and children of a single check, plus
// the soft configuration lint over its `dependsOn` edges.
type PerCheckDependencies struct {
	DependsOn    []DependencyResponse `json:"dependsOn"`
	DependedOnBy []DependencyResponse `json:"dependedOnBy"`
	// Warnings is never nil — an empty array, so a client can render it
	// without a null guard.
	Warnings []DependencyWarning `json:"warnings"`
}

// DependencyWarning is one soft lint finding about an edge. It carries the
// numbers behind the verdict rather than only prose, so the dashboard can
// render a localized sentence (and a concrete suggested value) without
// re-deriving the formula.
type DependencyWarning struct {
	Code          string   `json:"code"`
	DependencyUID string   `json:"dependencyUid"`
	ParentCheck   CheckRef `json:"parentCheck"`
	// ChildConfirmationSeconds is the child's configured confirmation period.
	ChildConfirmationSeconds int `json:"childConfirmationSeconds"`
	// RecommendedConfirmationSeconds is the strict margin
	// `parent.confirmation + parent.period + parent.timeoutOrDefault`,
	// rounded up to whole seconds.
	RecommendedConfirmationSeconds int `json:"recommendedConfirmationSeconds"`
	// Message is an English fallback for clients with no locale bundle; the
	// dashboard keys off Code instead.
	Message string `json:"message"`
}

// CreateDependencyRequest is the body for POST.
type CreateDependencyRequest struct {
	ParentCheckUID string  `json:"parentCheckUid"`
	Kind           string  `json:"kind"`
	Description    *string `json:"description,omitempty"`
}

// UpdateDependencyRequest is the body for PATCH.
type UpdateDependencyRequest struct {
	Kind        *string `json:"kind,omitempty"`
	Description *string `json:"description,omitempty"`
}

// GraphNode is one item in the org-graph response's nodes list.
type GraphNode struct {
	UID  string `json:"uid"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// GraphEdge is one item in the org-graph response's edges list.
type GraphEdge struct {
	UID            string `json:"uid"`
	ParentCheckUID string `json:"parentCheckUid"`
	ChildCheckUID  string `json:"childCheckUid"`
	Kind           string `json:"kind"`
}

// GraphResponse is the org-wide read-only graph.
type GraphResponse struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// resolveCheck loads a check by uid or slug; missing → ErrCheckNotFound.
func (s *Service) resolveCheck(ctx context.Context, orgUID, identifier string) (*models.Check, error) {
	check, err := s.db.GetCheckByUidOrSlug(ctx, orgUID, identifier)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrCheckNotFound
		}

		return nil, fmt.Errorf("get check: %w", err)
	}

	return check, nil
}

func (s *Service) resolveOrg(ctx context.Context, orgSlug string) (string, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return "", ErrOrganizationNotFound
	}

	return org.UID, nil
}

// ListForCheck returns the parents and children of the supplied check.
func (s *Service) ListForCheck(
	ctx context.Context, orgSlug, checkIdent string,
) (PerCheckDependencies, error) {
	orgUID, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return PerCheckDependencies{}, err
	}

	check, err := s.resolveCheck(ctx, orgUID, checkIdent)
	if err != nil {
		return PerCheckDependencies{}, err
	}

	parents, err := s.db.ListCheckDependencyParents(ctx, check.UID)
	if err != nil {
		return PerCheckDependencies{}, fmt.Errorf("list parents: %w", err)
	}

	children, err := s.db.ListCheckDependencyChildren(ctx, check.UID)
	if err != nil {
		return PerCheckDependencies{}, fmt.Errorf("list children: %w", err)
	}

	checkUIDs := collectCheckUIDs(parents, children)

	checks, err := s.loadChecks(ctx, orgUID, checkUIDs)
	if err != nil {
		return PerCheckDependencies{}, err
	}

	checkMap := checkRefs(checks)

	resp := PerCheckDependencies{
		DependsOn:    make([]DependencyResponse, 0, len(parents)),
		DependedOnBy: make([]DependencyResponse, 0, len(children)),
		Warnings:     []DependencyWarning{},
	}

	for _, dep := range parents {
		if edge, ok := resolvedDependencyResponse(dep, checkMap); ok {
			resp.DependsOn = append(resp.DependsOn, edge)
		}
	}

	for _, dep := range children {
		if edge, ok := resolvedDependencyResponse(dep, checkMap); ok {
			resp.DependedOnBy = append(resp.DependedOnBy, edge)
		}
	}

	resp.Warnings = s.confirmationMarginWarnings(check, parents, checks)

	return resp, nil
}

// confirmationMarginWarnings runs the soft confirmation-margin lint over the
// check's hard `dependsOn` edges (spec 2026-08-31-06).
//
// An edge is flagged when
//
//	child.confirmation < parent.confirmation + parent.period + parent.timeout
//
// i.e. when the child can finish confirming before its parent could possibly
// have observed the same outage even once — probe phase offset plus the
// parent's own connect timeout. That is the exact shape of the RabbitMQ
// outage this spec came from: parent and child were configured identically
// (60s period, 120s confirmation), the inequality `parent <= child` held on
// both counts, and four children still paged ahead of the parent.
//
// Soft edges are never linted: they do not suppress anything, so there is no
// ordering to protect.
func (s *Service) confirmationMarginWarnings(
	child *models.Check, parents []*models.CheckDependency, checks map[string]*models.Check,
) []DependencyWarning {
	out := []DependencyWarning{}

	for _, dep := range parents {
		if dep.Kind != models.CheckDependencyKindHard {
			continue
		}

		parent, ok := checks[dep.ParentCheckUID]
		if !ok || parent == nil {
			continue
		}

		margin := requiredConfirmationMargin(parent, s.defaultCheckTimeout)

		childConfirmation := time.Duration(child.ConfirmationPeriodSeconds) * time.Second
		if childConfirmation >= margin {
			continue
		}

		out = append(out, DependencyWarning{
			Code:          WarningCodeConfirmationMarginTooShort,
			DependencyUID: dep.UID,
			ParentCheck: CheckRef{
				UID:  parent.UID,
				Slug: derefString(parent.Slug),
				Name: derefString(parent.Name),
			},
			ChildConfirmationSeconds:       child.ConfirmationPeriodSeconds,
			RecommendedConfirmationSeconds: int(math.Ceil(margin.Seconds())),
			Message: "This check can confirm before its hard parent can possibly detect the same " +
				"outage; the confirmation hold will cover the gap at page time.",
		})
	}

	return out
}

// requiredConfirmationMargin is the strict margin from the spec:
// `parent.confirmation + parent.period + parent.timeoutOrDefault`. It is the
// worst case for "how long after an outage starts can the parent still be
// seeing its first failure" — one full period of phase offset plus the probe's
// own timeout, on top of the parent's confirmation window.
func requiredConfirmationMargin(parent *models.Check, defaultTimeout time.Duration) time.Duration {
	return time.Duration(parent.ConfirmationPeriodSeconds)*time.Second +
		time.Duration(parent.Period) +
		parent.TimeoutOrDefault(defaultTimeout)
}

func collectCheckUIDs(parents, children []*models.CheckDependency) []string {
	seen := make(map[string]struct{}, len(parents)+len(children))

	for _, dep := range parents {
		seen[dep.ParentCheckUID] = struct{}{}
		seen[dep.ChildCheckUID] = struct{}{}
	}

	for _, dep := range children {
		seen[dep.ParentCheckUID] = struct{}{}
		seen[dep.ChildCheckUID] = struct{}{}
	}

	uids := make([]string, 0, len(seen))
	for uid := range seen {
		uids = append(uids, uid)
	}

	return uids
}

// loadChecks resolves check UIDs to their full rows, skipping the ones that no
// longer exist. Full rows rather than bare refs: the confirmation-margin lint
// needs each parent's period / confirmation / timeout, and re-querying for
// them would double the number of round trips this endpoint makes.
func (s *Service) loadChecks(
	ctx context.Context, orgUID string, uids []string,
) (map[string]*models.Check, error) {
	out := make(map[string]*models.Check, len(uids))
	for _, uid := range uids {
		check, err := s.db.GetCheck(ctx, orgUID, uid)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}

			return nil, fmt.Errorf("get check ref: %w", err)
		}

		out[uid] = check
	}

	return out, nil
}

// checkRefs projects loaded check rows down to the {uid, slug, name} refs the
// API inlines into every edge.
func checkRefs(checks map[string]*models.Check) map[string]CheckRef {
	out := make(map[string]CheckRef, len(checks))
	for uid, check := range checks {
		out[uid] = CheckRef{UID: check.UID, Slug: derefString(check.Slug), Name: derefString(check.Name)}
	}

	return out
}

// loadCheckRefs resolves check UIDs straight to inline refs, for the paths
// that never lint (Update, Graph).
func (s *Service) loadCheckRefs(
	ctx context.Context, orgUID string, uids []string,
) (map[string]CheckRef, error) {
	checks, err := s.loadChecks(ctx, orgUID, uids)
	if err != nil {
		return nil, err
	}

	return checkRefs(checks), nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

func buildDependencyResponse(dep *models.CheckDependency, checks map[string]CheckRef) DependencyResponse {
	return DependencyResponse{
		UID:         dep.UID,
		ParentCheck: checks[dep.ParentCheckUID],
		ChildCheck:  checks[dep.ChildCheckUID],
		Kind:        string(dep.Kind),
		Description: dep.Description,
	}
}

// resolvedDependencyResponse builds a DependencyResponse and reports whether
// both endpoints resolved to a real check. An edge can survive in
// check_dependencies referencing a check that's since been (soft-)deleted —
// DeleteCheck cleans up matching edges going forward (see
// checks.Service.DeleteCheck), but this guards any row that predates that
// cleanup or otherwise slips through, so the UI never renders a check-less
// edge (see issue #129: a bare kind badge with no check name attached).
func resolvedDependencyResponse(dep *models.CheckDependency, checks map[string]CheckRef) (DependencyResponse, bool) {
	resp := buildDependencyResponse(dep, checks)
	if resp.ParentCheck.UID == "" || resp.ChildCheck.UID == "" {
		return DependencyResponse{}, false
	}

	return resp, true
}

// Create writes a new edge after validation: cross-org, self, duplicate, cycle.
func (s *Service) Create(
	ctx context.Context, orgSlug, childIdent string, req CreateDependencyRequest,
) (DependencyResponse, error) {
	orgUID, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return DependencyResponse{}, err
	}

	child, err := s.resolveCheck(ctx, orgUID, childIdent)
	if err != nil {
		return DependencyResponse{}, err
	}

	if req.ParentCheckUID == "" {
		return DependencyResponse{}, ErrCheckNotFound
	}

	parent, err := s.db.GetCheckByUidOrSlug(ctx, orgUID, req.ParentCheckUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DependencyResponse{}, ErrCrossOrg
		}

		return DependencyResponse{}, fmt.Errorf("get parent: %w", err)
	}

	if parent.OrganizationUID != orgUID {
		return DependencyResponse{}, ErrCrossOrg
	}

	if parent.UID == child.UID {
		return DependencyResponse{}, ErrSelfEdge
	}

	kind := models.CheckDependencyKind(req.Kind)
	if !kind.IsValid() {
		return DependencyResponse{}, ErrInvalidKind
	}

	existing, err := s.db.FindCheckDependencyEdge(ctx, parent.UID, child.UID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return DependencyResponse{}, fmt.Errorf("find edge: %w", err)
	}

	if existing != nil {
		return DependencyResponse{}, ErrDuplicate
	}

	if err := s.assertNoCycle(ctx, orgUID, parent.UID, child.UID); err != nil {
		return DependencyResponse{}, err
	}

	dep := models.NewCheckDependency(orgUID, parent.UID, child.UID, kind, req.Description)
	if err := s.db.CreateCheckDependency(ctx, dep); err != nil {
		return DependencyResponse{}, fmt.Errorf("create: %w", err)
	}

	checkMap := map[string]CheckRef{
		parent.UID: {UID: parent.UID, Slug: derefString(parent.Slug), Name: derefString(parent.Name)},
		child.UID:  {UID: child.UID, Slug: derefString(child.Slug), Name: derefString(child.Name)},
	}

	return buildDependencyResponse(dep, checkMap), nil
}

// assertNoCycle reports ErrCycle if adding (parent → child) would create a cycle.
// It does so by checking whether `parent` is already reachable from `child`
// in the existing graph (DFS).
func (s *Service) assertNoCycle(ctx context.Context, orgUID, parentUID, childUID string) error {
	const depthCap = 32

	deps, err := s.db.ListCheckDependenciesByOrg(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("list deps for cycle check: %w", err)
	}

	adjacency := make(map[string][]string, len(deps))
	for _, dep := range deps {
		adjacency[dep.ParentCheckUID] = append(adjacency[dep.ParentCheckUID], dep.ChildCheckUID)
	}

	stack := []string{childUID}
	visited := map[string]struct{}{childUID: {}}

	for depth := 0; depth < depthCap && len(stack) > 0; depth++ {
		next := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		for _, child := range adjacency[next] {
			if child == parentUID {
				return ErrCycle
			}

			if _, ok := visited[child]; ok {
				continue
			}

			visited[child] = struct{}{}
			stack = append(stack, child)
		}
	}

	return nil
}

// Update applies a PATCH.
func (s *Service) Update(
	ctx context.Context, orgSlug, depUID string, req UpdateDependencyRequest,
) (DependencyResponse, error) {
	orgUID, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return DependencyResponse{}, err
	}

	dep, err := s.db.GetCheckDependency(ctx, orgUID, depUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DependencyResponse{}, ErrDependencyNotFound
		}

		return DependencyResponse{}, fmt.Errorf("get dep: %w", err)
	}

	update := models.CheckDependencyUpdate{}

	if req.Kind != nil {
		kind := models.CheckDependencyKind(*req.Kind)
		if !kind.IsValid() {
			return DependencyResponse{}, ErrInvalidKind
		}

		update.Kind = &kind
	}

	if req.Description != nil {
		if *req.Description == "" {
			update.ClearDescription = true
		} else {
			desc := *req.Description
			update.Description = &desc
		}
	}

	if updateErr := s.db.UpdateCheckDependency(ctx, dep.UID, &update); updateErr != nil {
		return DependencyResponse{}, fmt.Errorf("update: %w", updateErr)
	}

	updated, err := s.db.GetCheckDependency(ctx, orgUID, depUID)
	if err != nil {
		return DependencyResponse{}, fmt.Errorf("reload: %w", err)
	}

	checkMap, err := s.loadCheckRefs(ctx, orgUID, []string{updated.ParentCheckUID, updated.ChildCheckUID})
	if err != nil {
		return DependencyResponse{}, err
	}

	return buildDependencyResponse(updated, checkMap), nil
}

// Delete soft-deletes the edge.
func (s *Service) Delete(ctx context.Context, orgSlug, depUID string) error {
	orgUID, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return err
	}

	dep, err := s.db.GetCheckDependency(ctx, orgUID, depUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDependencyNotFound
		}

		return fmt.Errorf("get dep: %w", err)
	}

	return s.db.DeleteCheckDependency(ctx, dep.UID)
}

// Graph returns the full org dependency graph (read-only).
func (s *Service) Graph(ctx context.Context, orgSlug string) (GraphResponse, error) {
	orgUID, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return GraphResponse{}, err
	}

	deps, err := s.db.ListCheckDependenciesByOrg(ctx, orgUID)
	if err != nil {
		return GraphResponse{}, fmt.Errorf("list deps: %w", err)
	}

	edges := make([]GraphEdge, 0, len(deps))
	uidSet := make(map[string]struct{}, 2*len(deps))

	for _, dep := range deps {
		edges = append(edges, GraphEdge{
			UID:            dep.UID,
			ParentCheckUID: dep.ParentCheckUID,
			ChildCheckUID:  dep.ChildCheckUID,
			Kind:           string(dep.Kind),
		})
		uidSet[dep.ParentCheckUID] = struct{}{}
		uidSet[dep.ChildCheckUID] = struct{}{}
	}

	uids := make([]string, 0, len(uidSet))
	for uid := range uidSet {
		uids = append(uids, uid)
	}

	checkMap, err := s.loadCheckRefs(ctx, orgUID, uids)
	if err != nil {
		return GraphResponse{}, err
	}

	nodes := make([]GraphNode, 0, len(checkMap))
	for uid := range checkMap {
		ref := checkMap[uid]
		nodes = append(nodes, GraphNode(ref))
	}

	return GraphResponse{Nodes: nodes, Edges: edges}, nil
}
