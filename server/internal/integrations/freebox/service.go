package freebox

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ErrNilSettings is returned when a service-layer helper is called
// without a settings struct — callers that mean "no settings" should
// pass a zero-valued struct, not nil.
var ErrNilSettings = errors.New("freebox: nil settings")

// StartPairing kicks off the Freebox pairing flow. The returned
// AuthorizeResult contains the permanent app_token and the short-lived
// track_id used to poll the LCD-approval status; both must be persisted
// onto the IntegrationConnection record by the caller (app_token
// encrypted, track_id in the public settings).
//
// The caller is expected to mutate `settings` to reflect the new state
// (Status = pairing, TrackID = result.TrackID); we leave that side
// effect to the caller so the service stays free of DB concerns.
func StartPairing(
	ctx context.Context, settings *models.FreeboxSettings,
) (*AuthorizeResult, error) {
	if settings == nil {
		return nil, ErrNilSettings
	}

	appID := settings.AppID
	if appID == "" {
		appID = DefaultAppID
	}

	deviceName := settings.DeviceName
	if deviceName == "" {
		deviceName = DefaultDeviceName
	}

	// We don't yet have an app_token; pass empty — Authorize doesn't
	// need it.
	client := NewClientWithAppID(settings.BaseURL, appID, "")

	result, err := client.Authorize(ctx, deviceName)
	if err != nil {
		return nil, fmt.Errorf("authorize: %w", err)
	}

	return result, nil
}

// CheckPairingStatus polls /login/authorize/{trackID} once and returns
// the raw status string (StatusUnknown / StatusPending / StatusGranted /
// StatusDenied / StatusTimeout). Returns ErrPairingTimeout /
// ErrPairingDenied when the Freebox itself confirms a terminal failure
// — those are the only two cases where re-pairing is required.
func CheckPairingStatus(
	ctx context.Context, settings *models.FreeboxSettings, trackID int,
) (string, error) {
	if settings == nil {
		return "", ErrNilSettings
	}

	client := NewClientWithAppID(settings.BaseURL, settings.AppID, "")

	status, err := client.PollPairing(ctx, trackID)
	if err != nil {
		return "", fmt.Errorf("poll pairing: %w", err)
	}

	switch status {
	case StatusGranted, StatusPending, StatusUnknown:
		return status, nil
	case StatusDenied:
		return status, ErrPairingDenied
	case StatusTimeout:
		return status, ErrPairingTimeout
	default:
		// Unknown status string — surface it so the UI can show it
		// verbatim while we keep the pairing record around.
		return status, nil
	}
}

// ValidateConnection opens a session against the Freebox using the
// stored app_token and immediately closes it (no side effect). Used by
// the channel handler when the operator clicks "test connection".
//
// Returns ErrAuthRequired if the Freebox no longer accepts the token
// (revoked from the admin), and the usual transport errors otherwise.
func ValidateConnection(
	ctx context.Context,
	settings *models.FreeboxSettings,
	priv *models.FreeboxPrivateSettings,
) error {
	if settings == nil {
		return ErrNilSettings
	}

	if priv == nil || priv.AppToken == "" {
		return ErrAuthRequired
	}

	client := NewClientWithAppID(settings.BaseURL, settings.AppID, priv.AppToken)

	if _, err := client.ensureSession(ctx); err != nil {
		return err
	}

	return nil
}

// lanBrowserPath is the path of the LAN browser endpoint. The "pub"
// interface is the main wired/Wi-Fi LAN — there are also "guest" and
// per-VLAN interfaces, but we only surface the main one in V1.
const lanBrowserPath = apiV4Prefix + "/lan/browser/pub/"

// ListLanHosts queries the Freebox LAN browser and returns a simplified,
// filtered list of hosts suitable for the dashboard's "Discover from
// Freebox" picker.
//
// Filter rules (mirror the spec):
//
//   - hosts with no active IPv4 connectivity are dropped (we can't ping
//     them anyway, and a check would be useless);
//   - hosts of type "router" are dropped — that's the Freebox itself,
//     and a Freebox→Freebox ICMP check is pointless;
//   - the most-recently-active IPv4 address is preferred when a host
//     exposes several;
//   - results are sorted reachable-first, then by name (case-insensitive
//     stable order, so the dashboard's display stays predictable
//     between polls).
//
// The client argument lets callers swap in a custom HTTP base (used by
// the channels handler, which constructs a per-channel client from the
// decrypted app_token).
func ListLanHosts(ctx context.Context, client *Client) ([]LanHost, error) {
	if client == nil {
		return nil, fmt.Errorf("%w: nil client", ErrNilSettings)
	}

	var raw []FreeboxLanHost
	if err := client.Get(ctx, lanBrowserPath, &raw); err != nil {
		return nil, fmt.Errorf("freebox: list lan hosts: %w", err)
	}

	hosts := make([]LanHost, 0, len(raw))

	for i := range raw {
		host, ok := simplifyLanHost(&raw[i])
		if !ok {
			continue
		}

		hosts = append(hosts, host)
	}

	sortLanHosts(hosts)

	return hosts, nil
}

// simplifyLanHost converts one Freebox LAN-host record to the
// dashboard-facing LanHost shape. Returns (_, false) when the host
// must be filtered out (router, no active IPv4, …).
func simplifyLanHost(raw *FreeboxLanHost) (LanHost, bool) {
	if raw.HostType == LanHostTypeRouter {
		return LanHost{}, false
	}

	addr, lastActivity, ok := pickBestIPv4(raw.L3Connectivities)
	if !ok {
		return LanHost{}, false
	}

	name := raw.PrimaryName
	if name == "" {
		// Fall back to the first named entry — better than an empty
		// label in the picker.
		for _, n := range raw.Names {
			if n.Name != "" {
				name = n.Name

				break
			}
		}
	}

	// Pick the latest "seen" timestamp: prefer the host's own
	// last_time_reachable when set, fall back to the chosen IPv4's
	// last_activity. Both are unix-seconds from the Freebox.
	lastSeen := raw.LastTimeReachable
	if lastActivity > lastSeen {
		lastSeen = lastActivity
	}

	var lastSeenStr string
	if lastSeen > 0 {
		lastSeenStr = time.Unix(lastSeen, 0).UTC().Format(time.RFC3339)
	}

	return LanHost{
		ID:        raw.ID,
		Name:      name,
		IP:        addr,
		HostType:  raw.HostType,
		Reachable: raw.Reachable,
		LastSeen:  lastSeenStr,
	}, true
}

// pickBestIPv4 returns the most-recently-active IPv4 address from a
// host's connectivity list, plus its last_activity timestamp. Inactive
// IPv4 entries are skipped: an active=false entry typically corresponds
// to a stale DHCP lease the Freebox is still tracking.
func pickBestIPv4(conns []FreeboxLanL3Conn) (string, int64, bool) {
	var (
		bestAddr  string
		bestStamp int64
		found     bool
	)

	for _, c := range conns {
		if c.Af != LanAfIPv4 || !c.Active || c.Addr == "" {
			continue
		}

		if !found || c.LastActivity > bestStamp {
			bestAddr = c.Addr
			bestStamp = c.LastActivity
			found = true
		}
	}

	return bestAddr, bestStamp, found
}

// sortLanHosts sorts hosts in place: reachable first, then by name
// (case-insensitive), then by ID as a final tiebreaker so the order is
// stable across polls of the same Freebox.
func sortLanHosts(hosts []LanHost) {
	sort.SliceStable(hosts, func(i, j int) bool {
		if hosts[i].Reachable != hosts[j].Reachable {
			return hosts[i].Reachable
		}

		ni, nj := lowerName(hosts[i].Name), lowerName(hosts[j].Name)
		if ni != nj {
			return ni < nj
		}

		return hosts[i].ID < hosts[j].ID
	})
}

// lowerName lowercases a name for case-insensitive sorting without
// pulling in the unicode/strings cost on every comparison. ASCII-only
// is enough — Freebox hostnames are DNS-bound.
func lowerName(s string) string {
	b := make([]byte, len(s))
	for i := range len(s) {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}

		b[i] = c
	}

	return string(b)
}
