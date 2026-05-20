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
	// This corresponds to a /20 IPv4 network.
	MaxAddresses = 4096

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
