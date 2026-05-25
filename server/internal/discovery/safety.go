// Package discovery provides network discovery functionality for solidping.
package discovery

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
)

const (
	// MaxAddresses is the maximum number of addresses that can be scanned in a single job.
	// This corresponds to a /20 IPv4 network. It stays the per-chunk cap for child
	// network_discovery jobs.
	MaxAddresses = 4096

	// MaxScanChunks is the fixed overall ceiling on how many ≤MaxAddresses chunks a single
	// fan-out scan may be split into. 256 chunks × 4096 addresses ≈ 1M addresses (a /12),
	// which keeps a /8 from accidentally scheduling thousands of child jobs.
	MaxScanChunks = 256

	// ErrCodeRangeTooLarge is the error code returned when the CIDRs expand to more than MaxAddresses.
	ErrCodeRangeTooLarge = "DISCOVERY_RANGE_TOO_LARGE"
)

// ErrRangeTooLarge is returned when the total address count across all CIDRs exceeds MaxAddresses.
var ErrRangeTooLarge = errors.New("discovery range too large")

// errNoCIDRs is returned when no CIDRs are provided.
var errNoCIDRs = errors.New("at least one CIDR is required")

// errInvalidMask is returned when the IP network has an invalid mask.
var errInvalidMask = errors.New("invalid mask")

// errIPv6NotSupported is returned when a non-IPv4 CIDR is provided.
var errIPv6NotSupported = errors.New("only IPv4 networks are supported in v1")

// ValidateCIDRs validates that the given CIDRs are valid and that the total
// address count does not exceed MaxAddresses.
func ValidateCIDRs(cidrs []string) error {
	if len(cidrs) == 0 {
		return errNoCIDRs
	}

	total := 0

	for _, cidr := range cidrs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}

		size, err := cidrSize(ipNet)
		if err != nil {
			return fmt.Errorf("cannot compute size for CIDR %q: %w", cidr, err)
		}

		total += size
	}

	if total > MaxAddresses {
		return fmt.Errorf("%w: %d addresses (max %d, use /20 or smaller)", ErrRangeTooLarge, total, MaxAddresses)
	}

	return nil
}

// ValidatePlanCIDRs validates the CIDRs for a fan-out scan plan. Each CIDR must
// be a valid IPv4 network, and the total number of ≤MaxAddresses chunks the scan
// would split into must not exceed MaxScanChunks. It returns ErrRangeTooLarge
// (wrapped, with an updated message) when the chunk count exceeds the ceiling.
func ValidatePlanCIDRs(cidrs []string) error {
	chunks, err := SplitCIDRs(cidrs, MaxAddresses)
	if err != nil {
		return err
	}

	if len(chunks) > MaxScanChunks {
		return fmt.Errorf(
			"%w: %d chunks (max %d, ≈%d addresses)",
			ErrRangeTooLarge, len(chunks), MaxScanChunks, MaxScanChunks*MaxAddresses,
		)
	}

	return nil
}

// SplitCIDRs subdivides each input CIDR into ≤ /20 (MaxAddresses) blocks and packs
// those blocks into chunks whose combined address count does not exceed maxPerChunk.
// Every returned chunk is itself a valid set of CIDR strings that ValidateCIDRs
// accepts, so each can drive an independent bounded network_discovery child job.
// CIDRs larger than maxPerChunk are split; smaller CIDRs are packed together.
func SplitCIDRs(cidrs []string, maxPerChunk int) ([][]string, error) {
	if len(cidrs) == 0 {
		return nil, errNoCIDRs
	}

	if maxPerChunk <= 0 {
		maxPerChunk = MaxAddresses
	}

	// First, normalize every input CIDR into ≤ maxPerChunk-sized blocks.
	blocks := make([]cidrBlock, 0, len(cidrs))

	for _, cidr := range cidrs {
		split, err := splitOneCIDR(cidr, maxPerChunk)
		if err != nil {
			return nil, err
		}

		blocks = append(blocks, split...)
	}

	// Then pack blocks greedily into chunks of ≤ maxPerChunk addresses.
	chunks := make([][]string, 0, 1)
	current := make([]string, 0, 1)
	currentSize := 0

	for _, blk := range blocks {
		if currentSize+blk.size > maxPerChunk && len(current) > 0 {
			chunks = append(chunks, current)
			current = make([]string, 0, 1)
			currentSize = 0
		}

		current = append(current, blk.cidr)
		currentSize += blk.size
	}

	if len(current) > 0 {
		chunks = append(chunks, current)
	}

	return chunks, nil
}

// cidrBlock is a single normalized CIDR block plus its address count.
type cidrBlock struct {
	cidr string
	size int
}

// splitOneCIDR subdivides a single CIDR into blocks no larger than maxPerChunk
// addresses. A CIDR already ≤ maxPerChunk is returned as one block; a larger CIDR
// is recursively halved (mask + 1 on each side) until every piece fits.
func splitOneCIDR(cidr string, maxPerChunk int) ([]cidrBlock, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
	}

	ip4 := ipNet.IP.To4()
	if ip4 == nil {
		return nil, fmt.Errorf("CIDR %q: %w", cidr, errIPv6NotSupported)
	}

	size, err := cidrSize(ipNet)
	if err != nil {
		return nil, fmt.Errorf("cannot compute size for CIDR %q: %w", cidr, err)
	}

	if size <= maxPerChunk {
		return []cidrBlock{{cidr: ipNet.String(), size: size}}, nil
	}

	ones, bits := ipNet.Mask.Size()

	// Split into two halves by extending the prefix by one bit.
	childOnes := ones + 1
	start := binary.BigEndian.Uint32(ip4)
	half := uint32(1) << uint(bits-childOnes)

	blocks := make([]cidrBlock, 0, 2)

	for _, base := range []uint32{start, start + half} {
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, base)
		childCIDR := fmt.Sprintf("%s/%d", net.IP(b).String(), childOnes)

		split, splitErr := splitOneCIDR(childCIDR, maxPerChunk)
		if splitErr != nil {
			return nil, splitErr
		}

		blocks = append(blocks, split...)
	}

	return blocks, nil
}

// cidrSize returns the number of host addresses in the network (all IPs including network and broadcast).
func cidrSize(ipNet *net.IPNet) (int, error) {
	ones, bits := ipNet.Mask.Size()
	if bits == 0 {
		return 0, errInvalidMask
	}

	// For IPv6 we don't support scanning, but we'll count anyway to reject.
	hostBits := bits - ones
	if hostBits >= 31 {
		// Prevent overflow; cap at a large number that exceeds MaxAddresses.
		return MaxAddresses + 1, nil
	}

	return 1 << hostBits, nil
}

// expandCIDR returns all IP addresses in the given CIDR range.
func expandCIDR(cidr string) ([]net.IP, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
	}

	var ips []net.IP

	// Convert to 4-byte representation.
	ip4 := ipNet.IP.To4()
	if ip4 == nil {
		return nil, fmt.Errorf("CIDR %q: %w", cidr, errIPv6NotSupported)
	}

	start := binary.BigEndian.Uint32(ip4)
	mask := binary.BigEndian.Uint32([]byte(ipNet.Mask))
	end := (start & mask) | (^mask)

	for n := start; n <= end; n++ {
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, n)
		ips = append(ips, net.IP(b))
	}

	return ips, nil
}
