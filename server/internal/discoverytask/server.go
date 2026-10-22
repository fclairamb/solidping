package discoverytask

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// Server-side limits.
const (
	// DefaultInventoryCap is how many active discovered checks an org may hold
	// before a chunk is refused: a dishonest agent cannot flood the database.
	DefaultInventoryCap = 20_000
	maxHostnameLength   = 253
	// maxRefusalEvents bounds the refusal events read to deduplicate one.
	maxRefusalEvents = 1000
)

// Audit payload keys.
const (
	payloadScanUID   = "scan_uid"
	payloadRegion    = "region"
	payloadErrorCode = "error_code"
	auditTargetAgent = "agent"
)

// Deps are what the server side of the kind needs.
type Deps struct {
	DB    db.Service
	Tasks *workertasks.Service
}

// Server is the server side of the discovery kind.
type Server struct {
	db           db.Service
	inventoryCap int
	logger       *slog.Logger
}

// Register registers the discovery kind on deps.Tasks and returns its server
// side.
func Register(deps Deps) *Server {
	srv := &Server{
		db:           deps.DB,
		inventoryCap: DefaultInventoryCap,
		logger:       slog.Default().With("component", "discovery_task"),
	}

	deps.Tasks.RegisterKind(workertasks.TaskKind{Name: Kind, Complete: srv.complete})

	return srv
}

// SetInventoryCap changes the org inventory cap (tests).
func (s *Server) SetInventoryCap(limit int) {
	s.inventoryCap = limit
}

// complete checks an agent's result against its chunk, rebuilds the suggested
// checks and stores them under the plan, in the task's region.
func (s *Server) complete(ctx context.Context, task *models.WorkerTask, body json.RawMessage) (string, error) {
	var result Result
	if err := json.Unmarshal(body, &result); err != nil {
		return codeInvalidResult, nil
	}

	if result.ErrorCode != "" {
		return s.refused(ctx, task, result.ErrorCode), nil
	}

	input, err := taskInput(task)
	if err != nil {
		return codeInvalidResult, nil //nolint:nilerr // a malformed payload fails the task, not the request
	}

	hosts, ok := filterHosts(input, result.Hosts)
	if !ok {
		s.logger.WarnContext(ctx, "Discovery result rejected: more hosts than addresses",
			"task", task.UID, "hosts", len(result.Hosts))

		return codeInvalidResult, nil
	}

	rows := make([]disc.SuggestedCheck, 0, len(hosts))
	for i := range hosts {
		rows = append(rows, disc.SuggestChecks(hosts[i].IP, hosts[i].Hostname, hosts[i].ICMPReachable,
			hosts[i].OpenPorts)...)
	}

	full, err := s.inventoryFull(ctx, task.OrganizationUID, len(rows))
	if err != nil {
		return "", err
	}

	if full {
		s.logger.WarnContext(ctx, "Discovery result refused: org inventory full", "task", task.UID)

		return codeInventoryFull, nil
	}

	if task.JobUID == nil {
		return codeInvalidResult, nil
	}

	if err := disc.UpsertDiscoveredChecks(ctx, s.db.DB(), task.OrganizationUID, *task.JobUID, task.Region,
		models.DiscoverySourceLAN, rows, s.logger); err != nil {
		return "", fmt.Errorf("store discovered checks: %w", err)
	}

	if _, err := s.db.DB().NewUpdate().Model((*models.WorkerTask)(nil)).
		Set("progress = ?", models.JSONMap{"hostCount": len(hosts), "icmpAvailable": result.ICMPAvailable}).
		Where("uid = ?", task.UID).
		Exec(ctx); err != nil {
		return "", fmt.Errorf("record discovery progress: %w", err)
	}

	if result.Partial {
		return codePartial, nil
	}

	return "", nil
}

// refused records an agent's terminal refusal: the code it sent when it is one
// of the known ones, and one audit event per (plan, agent).
func (s *Server) refused(ctx context.Context, task *models.WorkerTask, code string) string {
	if !isRefusal(code) {
		return codeInvalidResult
	}

	if task.WorkerUID == nil || task.JobUID == nil {
		return code
	}

	agentUID := *task.WorkerUID

	// The plan's events are all after the plan itself.
	since := task.CreatedAt
	if plan, err := s.db.GetJob(ctx, *task.JobUID); err == nil && plan != nil {
		since = plan.CreatedAt
	}

	events, err := s.db.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: task.OrganizationUID,
		EventTypes:      []models.EventType{models.EventTypeDiscoveryChunkRefused},
		TargetUID:       &agentUID,
		Since:           &since,
		Limit:           maxRefusalEvents,
	})
	if err != nil {
		s.logger.WarnContext(ctx, "Failed to read discovery refusal events", "task", task.UID, "error", err)

		return code
	}

	for _, event := range events {
		if event.Payload[payloadScanUID] == *task.JobUID {
			return code
		}
	}

	audit.Record(audit.WithActor(ctx, audit.Actor{Type: models.ActorTypeSystem}), s.db, task.OrganizationUID,
		models.EventTypeDiscoveryChunkRefused, audit.Target{Type: auditTargetAgent, UID: agentUID},
		models.JSONMap{payloadScanUID: *task.JobUID, payloadRegion: task.Region, payloadErrorCode: code})

	return code
}

func taskInput(task *models.WorkerTask) (*Input, error) {
	raw, err := json.Marshal(task.Payload)
	if err != nil {
		return nil, fmt.Errorf("encode discovery payload: %w", err)
	}

	var input Input
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, fmt.Errorf("decode discovery payload: %w", err)
	}

	return &input, nil
}

// filterHosts keeps the hosts whose IP is IPv4 and inside the chunk, once
// each, with only the requested ports and a sane hostname. It reports false
// when the body names more distinct hosts than the chunk has addresses.
func filterHosts(input *Input, hosts []HostFact) ([]HostFact, bool) {
	prefixes := make([]netip.Prefix, 0, len(input.CIDRs))
	for _, cidr := range input.CIDRs {
		if prefix, err := netip.ParsePrefix(cidr); err == nil && prefix.Addr().Is4() {
			prefixes = append(prefixes, prefix.Masked())
		}
	}

	distinct := make(map[string]bool, len(hosts))
	for i := range hosts {
		distinct[hosts[i].IP] = true
	}

	if len(distinct) > addressCount(prefixes) {
		return nil, false
	}

	seen := make(map[netip.Addr]bool, len(hosts))
	out := make([]HostFact, 0, len(hosts))

	for i := range hosts {
		addr, err := netip.ParseAddr(hosts[i].IP)
		if err != nil || !addr.Is4() || seen[addr] || !containedIn(prefixes, addr) {
			continue
		}

		seen[addr] = true
		out = append(out, HostFact{
			IP:            addr.String(),
			Hostname:      cleanHostname(hosts[i].Hostname),
			ICMPReachable: hosts[i].ICMPReachable,
			OpenPorts:     requestedPorts(input.Ports, hosts[i].OpenPorts),
		})
	}

	return out, true
}

// addressCount counts the distinct addresses of the prefixes.
func addressCount(prefixes []netip.Prefix) int {
	total := 0

	for i, prefix := range prefixes {
		covered := false

		for j, other := range prefixes {
			if j != i && other.Bits() <= prefix.Bits() && other.Contains(prefix.Addr()) &&
				(other.Bits() < prefix.Bits() || j < i) {
				covered = true

				break
			}
		}

		if !covered {
			total += 1 << (32 - prefix.Bits())
		}
	}

	return total
}

func containedIn(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}

	return false
}

func requestedPorts(requested, open []int) []int {
	out := make([]int, 0, len(open))

	for _, port := range open {
		if slices.Contains(requested, port) && !slices.Contains(out, port) {
			out = append(out, port)
		}
	}

	slices.Sort(out)

	return out
}

// cleanHostname empties a name longer than 253 characters or outside
// [a-z0-9.-] (after lowercasing).
func cleanHostname(name string) string {
	name = strings.ToLower(name)
	if len(name) > maxHostnameLength {
		return ""
	}

	for _, char := range name {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '.' && char != '-' {
			return ""
		}
	}

	return name
}

// inventoryFull reports whether adding rows would take the org over its cap of
// active (not dismissed, not promoted) discovered checks.
func (s *Server) inventoryFull(ctx context.Context, orgUID string, rows int) (bool, error) {
	count, err := s.db.DB().NewSelect().
		TableExpr("discovered_checks").
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Where("promoted_to_check_uid IS NULL").
		Count(ctx)
	if err != nil {
		return false, fmt.Errorf("count discovered checks: %w", err)
	}

	return count+rows > s.inventoryCap, nil
}
