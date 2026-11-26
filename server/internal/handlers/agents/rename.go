package agents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	plconfig "github.com/fclairamb/solidping/server/internal/checkers/checkprivatelocation/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/regions"
)

const (
	// MaxNameLength bounds an agent or private location name, in characters.
	MaxNameLength = 100
	// maxEmojiLength bounds a private location emoji, in characters (a ZWJ
	// sequence is several code points).
	maxEmojiLength = 16
	// defaultRegionEmoji is the emoji of a private location that has none.
	defaultRegionEmoji = "🏠"
)

// ErrInvalidName refuses an empty or oversized agent or location name.
var ErrInvalidName = fmt.Errorf("name must be 1 to %d characters", MaxNameLength)

// ErrInvalidEmoji refuses an oversized private location emoji.
var ErrInvalidEmoji = fmt.Errorf("emoji must be at most %d characters", maxEmojiLength)

// UpdateAgentRequest is the body of PATCH /orgs/:org/agents/:uid.
type UpdateAgentRequest struct {
	Name string `json:"name"`
}

// UpdatePrivateRegionRequest is the body of PATCH /orgs/:org/private-regions/:slug.
// An omitted field is left unchanged.
type UpdatePrivateRegionRequest struct {
	Name  *string `json:"name,omitempty"`
	Emoji *string `json:"emoji,omitempty"`
}

func normalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > MaxNameLength {
		return "", ErrInvalidName
	}

	return name, nil
}

// UpdateAgent renames one of the org's agents. The name is display only: the
// agent keeps its identity and is not told (its SP_AGENT_NAME is only sent at
// enrollment).
func (s *Service) UpdateAgent(
	ctx context.Context, orgSlug, uid string, req *UpdateAgentRequest,
) (*AgentResponse, error) {
	org, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	name, err := normalizeName(req.Name)
	if err != nil {
		return nil, err
	}

	// OrgUID() is "" for a system agent, which therefore never matches.
	agent, err := s.db.GetAgent(ctx, uid)
	if err != nil || agent.OrgUID() != org.UID {
		return nil, fmt.Errorf("%w: %s", ErrAgentNotFound, uid)
	}

	if renameErr := s.db.RenameAgent(ctx, org.UID, uid, name); renameErr != nil {
		if errors.Is(renameErr, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrAgentNotFound, uid)
		}

		return nil, renameErr
	}

	list, err := s.ListAgents(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	for i := range list.Data {
		if list.Data[i].UID == uid {
			return &list.Data[i], nil
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrAgentNotFound, uid)
}

// UpdatePrivateRegion changes a private location's name and/or emoji. The
// slug is the location's identity (agents, tokens, keys and checks reference
// it) and cannot change here.
func (s *Service) UpdatePrivateRegion(
	ctx context.Context, orgSlug, slug string, req *UpdatePrivateRegionRequest,
) (*PrivateRegionResponse, error) {
	org, err := s.resolveOrg(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	defs, err := s.regions.GetOrgCustomRegions(ctx, org.UID)
	if err != nil {
		return nil, err
	}

	idx := -1

	for i := range defs {
		if defs[i].Slug == slug {
			idx = i

			break
		}
	}

	if idx < 0 {
		return nil, fmt.Errorf("%w: %s", ErrRegionNotFound, slug)
	}

	oldName := regionDisplayName(&defs[idx])

	if req.Name != nil {
		name, nameErr := normalizeName(*req.Name)
		if nameErr != nil {
			return nil, nameErr
		}

		defs[idx].Name = name
	}

	if req.Emoji != nil {
		emoji := strings.TrimSpace(*req.Emoji)
		if len([]rune(emoji)) > maxEmojiLength {
			return nil, ErrInvalidEmoji
		}

		if emoji == "" {
			emoji = defaultRegionEmoji
		}

		defs[idx].Emoji = emoji
	}

	if setErr := s.regions.SetOrgCustomRegions(ctx, org.UID, defs); setErr != nil {
		return nil, setErr
	}

	s.renameLivenessMonitor(ctx, org.UID, slug, oldName, regionDisplayName(&defs[idx]))

	list, err := s.ListPrivateRegions(ctx, orgSlug)
	if err != nil {
		return nil, err
	}

	for i := range list.Data {
		if list.Data[i].Slug == slug {
			return &list.Data[i], nil
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrRegionNotFound, slug)
}

// regionDisplayName is the name a location's liveness monitor was named after.
func regionDisplayName(def *regions.RegionDefinition) string {
	if def.Name != "" {
		return def.Name
	}

	return def.Slug
}

// renameLivenessMonitor follows a location rename on its liveness monitor,
// unless the monitor was renamed by hand. Best effort: the location is renamed
// either way.
func (s *Service) renameLivenessMonitor(ctx context.Context, orgUID, slug, oldName, newName string) {
	if s.monitors == nil || oldName == newName {
		return
	}

	monitors, err := s.monitors.PrivateLocationMonitors(ctx, orgUID)
	if err != nil {
		slog.WarnContext(ctx, "failed to list liveness monitors for a location rename", "region", slug, "error", err)

		return
	}

	monitor := monitors[slug]
	if monitor == nil || monitor.Name == nil || *monitor.Name != plconfig.NamePrefix+oldName {
		return
	}

	name := plconfig.NamePrefix + newName
	if err := s.db.UpdateCheck(ctx, monitor.UID, &models.CheckUpdate{Name: &name}); err != nil {
		slog.WarnContext(ctx, "failed to rename the location's liveness monitor", "region", slug, "error", err)
	}
}
