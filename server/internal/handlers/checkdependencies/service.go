// Package checkdependencies provides CRUD over the check_dependencies graph
// and the helpers used by the incident-rollup hook.
package checkdependencies

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

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
}

// NewService creates a new dependency service.
func NewService(dbService db.Service) *Service {
	return &Service{db: dbService}
}

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

// PerCheckDependencies bundles parents and children of a single check.
type PerCheckDependencies struct {
	DependsOn    []DependencyResponse `json:"dependsOn"`
	DependedOnBy []DependencyResponse `json:"dependedOnBy"`
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

	checkMap, err := s.loadCheckRefs(ctx, orgUID, checkUIDs)
	if err != nil {
		return PerCheckDependencies{}, err
	}

	resp := PerCheckDependencies{
		DependsOn:    make([]DependencyResponse, 0, len(parents)),
		DependedOnBy: make([]DependencyResponse, 0, len(children)),
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

	return resp, nil
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

func (s *Service) loadCheckRefs(
	ctx context.Context, orgUID string, uids []string,
) (map[string]CheckRef, error) {
	out := make(map[string]CheckRef, len(uids))
	for _, uid := range uids {
		check, err := s.db.GetCheck(ctx, orgUID, uid)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}

			return nil, fmt.Errorf("get check ref: %w", err)
		}

		out[uid] = CheckRef{UID: check.UID, Slug: derefString(check.Slug), Name: derefString(check.Name)}
	}

	return out, nil
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
