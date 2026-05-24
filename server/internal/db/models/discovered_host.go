package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// DiscoverySource identifies which discovery mechanism found a host.
// It is a named string (matching the ProviderType / JobType convention) so
// the set of sources is closed in code while staying a plain TEXT column.
type DiscoverySource string

// Supported discovery sources.
const (
	// DiscoverySourceLAN marks a host found by the CIDR network scanner.
	DiscoverySourceLAN DiscoverySource = "lan"
	// DiscoverySourceFreebox marks a host found via the Freebox LAN browser.
	DiscoverySourceFreebox DiscoverySource = "freebox"
)

// DiscoveredHost represents a host found during a network discovery scan.
type DiscoveredHost struct {
	UID                string          `bun:"uid,pk,type:varchar(36)"                         json:"uid"`
	OrganizationUID    string          `bun:"organization_uid,notnull"                        json:"organizationUid"`
	JobUID             string          `bun:"job_uid,notnull"                                 json:"jobUid"`
	IP                 string          `bun:"ip,notnull"                                      json:"ip"`
	Hostname           string          `bun:"hostname"                                        json:"hostname,omitempty"`
	OpenPorts          json.RawMessage `bun:"open_ports,type:jsonb"                           json:"openPorts"`
	ICMPReachable      bool            `bun:"icmp_reachable,notnull"                          json:"icmpReachable"`
	SuggestedChecks    json.RawMessage `bun:"suggested_checks,type:jsonb"                     json:"suggestedChecks"`
	Source             DiscoverySource `bun:"source,notnull"                                  json:"source"`
	PromotedToCheckUID *string         `bun:"promoted_to_check_uid"                           json:"promotedToCheckUid,omitempty"` //nolint:lll
	DiscoveredAt       time.Time       `bun:"discovered_at,notnull,default:current_timestamp" json:"discoveredAt"`
	DeletedAt          *time.Time      `bun:"deleted_at"                                      json:"deletedAt,omitempty"`
}

// NewDiscoveredHost creates a new DiscoveredHost with a generated UID.
func NewDiscoveredHost(orgUID, jobUID, ip string, source DiscoverySource) *DiscoveredHost {
	return &DiscoveredHost{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		JobUID:          jobUID,
		IP:              ip,
		OpenPorts:       json.RawMessage("[]"),
		SuggestedChecks: json.RawMessage("[]"),
		Source:          source,
		DiscoveredAt:    time.Now(),
	}
}
