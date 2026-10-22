// Package discoverytask runs LAN discovery chunks on an agent of a private
// region (spec 2026-10-22-04). A region-bound LAN scan is a
// network_discovery_agent_plan job that fans out into worker_tasks rows of kind
// "discovery", one per chunk of at most discovery.MaxAddresses addresses,
// claimed over the generic agent task routes.
//
// The agent (Executor) checks the chunk against its operator's allow-list and
// reports raw facts only. The server (Server) checks those facts against the
// chunk, rebuilds the suggested checks itself and stores them under the plan,
// in the scan's region.
package discoverytask

// Names of the kind.
const (
	// Kind is the worker_tasks.kind of a discovery chunk.
	Kind = "discovery"
	// Capability is what an agent running the executor reports.
	Capability = "discovery"
)

// Terminal codes an agent sends with a refused chunk.
const (
	// CodeNotAllowed is a chunk outside the agent's allow-list.
	CodeNotAllowed = "DISCOVERY_NOT_ALLOWED"
	// CodeDisabled is a chunk sent to an agent whose discovery is off.
	CodeDisabled = "DISCOVERY_DISABLED"
	// CodeEgressRefused is an agent whose egress guard refuses private targets.
	CodeEgressRefused = "DISCOVERY_EGRESS_REFUSED"
	// CodeInvalidChunk is a malformed chunk.
	CodeInvalidChunk = "DISCOVERY_INVALID_CHUNK"
)

// Codes the server writes on a failed chunk.
const (
	// codeInvalidResult is a result body the server rejected as a whole.
	codeInvalidResult = "invalid_result"
	// codePartial is a chunk cut short (deadline or cancel): its hosts are kept.
	codePartial = "partial"
	// codeInventoryFull is a chunk refused because the org holds too many
	// active discovered checks.
	codeInventoryFull = "inventory_full"
)

// Input is the claim input of a chunk, and the task payload.
type Input struct {
	CIDRs []string `json:"cidrs"`
	Ports []int    `json:"ports"`
}

// PlanConfig is the config of a network_discovery_agent_plan job.
type PlanConfig struct {
	CIDRs  []string `json:"cidrs"`
	Ports  []int    `json:"ports"`
	Region string   `json:"region"`
	// AddressCount is how many addresses the CIDRs cover: what the plan
	// charges against the org's daily agent quota.
	AddressCount int `json:"addressCount"`
}

// HostFact is what an agent saw of one host: raw facts, never a check config.
type HostFact struct {
	IP            string `json:"ip"`
	Hostname      string `json:"hostname,omitempty"`
	ICMPReachable bool   `json:"icmpReachable"`
	OpenPorts     []int  `json:"openPorts"`
}

// Result is the finish body of a chunk.
type Result struct {
	Hosts         []HostFact `json:"hosts"`
	Partial       bool       `json:"partial,omitempty"`
	ICMPAvailable bool       `json:"icmpAvailable"`
	ErrorCode     string     `json:"errorCode,omitempty"`
}

// MaxPorts caps the ports of one chunk, on both sides.
const MaxPorts = 32

func isRefusal(code string) bool {
	switch code {
	case CodeNotAllowed, CodeDisabled, CodeEgressRefused, CodeInvalidChunk:
		return true
	}

	return false
}
