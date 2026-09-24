package checkworker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	plconfig "github.com/fclairamb/solidping/server/internal/checkers/checkprivatelocation/config"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// Output keys of a private-location evaluation row (spec 2026-09-25-05).
const (
	outputKeyRegion               = "region"
	outputKeyAgentsTotal          = "agentsTotal"
	outputKeyAgentsOnline         = "agentsOnline"
	outputKeyOfflineAgents        = "offlineAgents"
	outputKeyLastSeenAt           = "lastSeenAt"
	outputKeyLastDisconnectAt     = "lastDisconnectAt"
	outputKeyLastDisconnectReason = "lastDisconnectReason"
	outputKeyLastDisconnectAgent  = "lastDisconnectAgent"
	outputKeyLastDisconnectEvent  = "lastDisconnectEventUid"
	offlineAgentKeyName           = "name"
	offlineAgentKeyLastSeenAt     = "lastSeenAt"
)

// errPrivateLocationNotEvaluable is returned when a private-location job
// reaches a backend with no database access (a deported agent). Only the jobs
// node can tell whether a location's agents are connected.
var errPrivateLocationNotEvaluable = errors.New(
	"private-location checks are evaluated by the jobs node, not by a check worker or agent")

// privateLocationVerdict evaluates a private-location liveness monitor. It is
// dispatched from passiveVerdict by check type, so it runs inside the same
// jobs-node PassiveEvaluator loop as heartbeat and email (spec 2026-09-25-04)
// and inherits its lease, schedule and result pipeline.
//
// grace=true means "no active agent was ever enrolled and the monitor never
// produced a result": write nothing, the check stays `created`, no incident.
func privateLocationVerdict(
	ctx context.Context, be backend.WorkerBackend, checkJob *models.CheckJob, now time.Time,
) (checkerdef.Status, map[string]any, bool, error) {
	reader, ok := be.(backend.PrivateLocationReader)
	if !ok {
		return 0, nil, false, errPrivateLocationNotEvaluable
	}

	region := privateLocationRegion(checkJob)

	agents, err := reader.PrivateLocationAgents(ctx, checkJob.OrganizationUID, region)
	if err != nil {
		return 0, nil, false, fmt.Errorf("failed to list the location's agents: %w", err)
	}

	everEvaluated := checkJob.Check != nil && checkJob.Check.LastResultAt != nil

	status, output, grace := privateLocationEvaluation(region, agents, everEvaluated, now)
	if grace {
		return 0, nil, true, nil
	}

	if status == checkerdef.StatusDown {
		// Best effort: the verdict stands without it.
		if event, evErr := reader.LastAgentDisconnect(ctx, checkJob.OrganizationUID, region); evErr == nil && event != nil {
			attachLastDisconnect(output, event, now)
		}
	}

	return status, output, false, nil
}

// privateLocationRegion reads the watched region off the job's config.
func privateLocationRegion(checkJob *models.CheckJob) string {
	if checkJob.Config == nil {
		return ""
	}

	region, _ := checkJob.Config[plconfig.ConfigKeyRegion].(string)

	return region
}

// privateLocationEvaluation is the verdict table of spec 2026-09-25-05 §1, a
// pure function of the location's agents. Liveness is regions.IsAgentLive —
// the rule region health uses (spec 2026-09-25-01) — never a second copy.
//
//   - every active agent live            -> Up      "2 agents connected"
//   - some active agents live, some not  -> Warning "1 of 2 agents offline: office-2 last seen 13:41 UTC"
//   - no active agent live               -> Down    "No agent connected since 13:41 UTC"
//   - no active agent, never evaluated   -> grace   (created, no incident)
//
// "Never evaluated" is what makes the last row mean "not enrolled YET": once
// the monitor has produced a result, losing every agent (revoked, purged) is
// an outage of the location, not a fresh location waiting for its first
// agent.
func privateLocationEvaluation(
	region string, agents []*models.Agent, everEvaluated bool, now time.Time,
) (checkerdef.Status, map[string]any, bool) {
	cutoff := regions.LivenessCutoff(now)

	var (
		active   []*models.Agent
		offline  []*models.Agent
		lastSeen *time.Time
	)

	for _, agent := range agents {
		if agent.LastSeenAt != nil && (lastSeen == nil || agent.LastSeenAt.After(*lastSeen)) {
			lastSeen = agent.LastSeenAt
		}

		if agent.Status != models.AgentStatusActive {
			continue
		}

		active = append(active, agent)

		if !regions.IsAgentLive(agent.Status, agent.LastSeenAt, cutoff) {
			offline = append(offline, agent)
		}
	}

	if len(active) == 0 && !everEvaluated {
		return 0, nil, true
	}

	online := len(active) - len(offline)

	output := map[string]any{
		outputKeyEvaluation:   true,
		outputKeyRegion:       region,
		outputKeyAgentsTotal:  len(active),
		outputKeyAgentsOnline: online,
	}

	if lastSeen != nil {
		output[outputKeyLastSeenAt] = lastSeen.UTC().Format(time.RFC3339)
	}

	switch {
	case len(active) > 0 && online == len(active):
		output[outputKeyMessage] = agentsConnectedMessage(online)

		return checkerdef.StatusUp, output, false

	case online > 0:
		output[outputKeyOfflineAgents] = offlineAgentsOutput(offline)
		output[outputKeyMessage] = fmt.Sprintf("%d of %d agents offline: %s",
			len(offline), len(active), offlineAgentsPhrase(offline, now))

		return checkerdef.StatusWarning, output, false

	default:
		if len(offline) > 0 {
			output[outputKeyOfflineAgents] = offlineAgentsOutput(offline)
		}

		if lastSeen == nil {
			output[outputKeyMessage] = "No agent connected"
		} else {
			output[outputKeyMessage] = "No agent connected since " + formatSeenAt(*lastSeen, now)
		}

		return checkerdef.StatusDown, output, false
	}
}

// agentsConnectedMessage is the Up message: "1 agent connected" / "2 agents
// connected".
func agentsConnectedMessage(count int) string {
	if count == 1 {
		return "1 agent connected"
	}

	return fmt.Sprintf("%d agents connected", count)
}

// offlineAgentsPhrase names the stale agents, oldest-seen first:
// "office-2 last seen 13:41 UTC, office-3 never seen".
func offlineAgentsPhrase(offline []*models.Agent, now time.Time) string {
	sorted := sortedOffline(offline)
	parts := make([]string, 0, len(sorted))

	for _, agent := range sorted {
		if agent.LastSeenAt == nil {
			parts = append(parts, agent.Name+" never seen")

			continue
		}

		parts = append(parts, agent.Name+" last seen "+formatSeenAt(*agent.LastSeenAt, now))
	}

	return strings.Join(parts, ", ")
}

// offlineAgentsOutput is the structured list of stale agents.
func offlineAgentsOutput(offline []*models.Agent) []map[string]any {
	sorted := sortedOffline(offline)
	out := make([]map[string]any, 0, len(sorted))

	for _, agent := range sorted {
		entry := map[string]any{offlineAgentKeyName: agent.Name}
		if agent.LastSeenAt != nil {
			entry[offlineAgentKeyLastSeenAt] = agent.LastSeenAt.UTC().Format(time.RFC3339)
		}

		out = append(out, entry)
	}

	return out
}

// sortedOffline orders stale agents by name, for a stable message.
func sortedOffline(offline []*models.Agent) []*models.Agent {
	sorted := append([]*models.Agent(nil), offline...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	return sorted
}

// formatSeenAt renders a last-seen instant the way the monitor's messages
// quote it: "13:41 UTC" today, "2026-09-24 13:41 UTC" on an earlier day (a
// bare time of day would read as today's).
func formatSeenAt(seen, now time.Time) string {
	seen = seen.UTC()
	now = now.UTC()

	if seen.Format(time.DateOnly) == now.Format(time.DateOnly) {
		return seen.Format("15:04") + " UTC"
	}

	return seen.Format("2006-01-02 15:04") + " UTC"
}

// disconnectReasonLabels words the recorded disconnect reasons for the
// monitor's message.
//
//nolint:gochecknoglobals // static lookup table
var disconnectReasonLabels = map[string]string{
	models.AgentDisconnectReasonPingTimeout:    "ping timeout",
	models.AgentDisconnectReasonRevoked:        "revoked",
	models.AgentDisconnectReasonServerShutdown: "server shutdown",
	models.AgentDisconnectReasonError:          "error",
}

// attachLastDisconnect links a Down evaluation to the location's last
// recorded disconnect (spec 2026-09-25-05 §3).
func attachLastDisconnect(output map[string]any, event *models.Event, now time.Time) {
	reason, _ := event.Payload[models.AgentEventPayloadReason].(string)
	if reason == "" {
		return
	}

	output[outputKeyLastDisconnectReason] = reason
	output[outputKeyLastDisconnectAt] = event.CreatedAt.UTC().Format(time.RFC3339)
	output[outputKeyLastDisconnectEvent] = event.UID

	if name, _ := event.Payload[audit.PayloadKeyTargetName].(string); name != "" {
		output[outputKeyLastDisconnectAgent] = name
	}

	label := disconnectReasonLabels[reason]
	if label == "" {
		label = reason
	}

	if message, ok := output[outputKeyMessage].(string); ok {
		output[outputKeyMessage] = fmt.Sprintf("%s (last disconnect: %s at %s)",
			message, label, formatSeenAt(event.CreatedAt, now))
	}
}
