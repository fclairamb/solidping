// Package dnsblquery holds the DNS blocklist (DNSBL) query primitives shared by
// the dnsbl checker and the email check's sender-authentication step: reversing
// an IP into its query form, classifying a zone's answer (a genuine listing
// versus a reserved 127.255.255.x error reply) and querying one IP against a
// set of zones.
//
// A DNSBL inverts the usual DNS semantics: an A-record answer means the IP IS
// listed, while NXDOMAIN means it is not.
package dnsblquery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
)

// Lookuper is the minimal resolver surface a query needs. *net.Resolver
// satisfies it; tests inject a map-backed fake.
type Lookuper interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// ReverseIPv4 returns the reversed-octet form of an IPv4 address used for DNSBL
// queries (1.2.3.4 -> 4.3.2.1). It returns "" for non-IPv4 input.
func ReverseIPv4(ipStr string) string {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return ""
	}

	ip4 := ip.To4()
	if ip4 == nil {
		return ""
	}

	return fmt.Sprintf("%d.%d.%d.%d", ip4[3], ip4[2], ip4[1], ip4[0])
}

// ReverseIPv6 returns the reversed-nibble form of an IPv6 address. It returns ""
// for non-IPv6 input (an IPv4 address, even in its mapped form, is not IPv6).
func ReverseIPv6(ipStr string) string {
	ip := net.ParseIP(ipStr)
	if ip == nil || ip.To4() != nil {
		return ""
	}

	ip16 := ip.To16()
	if ip16 == nil {
		return ""
	}

	const hexDigits = "0123456789abcdef"

	var sb strings.Builder

	for i := len(ip16) - 1; i >= 0; i-- {
		sb.WriteByte(hexDigits[ip16[i]&0x0f])
		sb.WriteByte('.')
		sb.WriteByte(hexDigits[ip16[i]>>4])

		if i > 0 {
			sb.WriteByte('.')
		}
	}

	return sb.String()
}

// ReverseIP returns the DNSBL query form of an IPv4 or IPv6 address.
func ReverseIP(ipStr string) string {
	if r := ReverseIPv4(ipStr); r != "" {
		return r
	}

	return ReverseIPv6(ipStr)
}

// IsErrorCode reports whether addr is a reserved DNSBL error/status reply.
//
// DNSBLs use the 127.255.255.0/24 range for non-listing replies rather than
// genuine listings — Spamhaus, for example, returns 127.255.255.252 (typo /
// malformed zone), 127.255.255.254 (query via a public/open resolver, refused),
// and 127.255.255.255 (query volume / rate-limit exceeded). No legitimate DNSBL
// encodes a real listing in 127.255.255.x, so the whole /24 is treated as an
// error range.
func IsErrorCode(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}

	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}

	return ip4[0] == 127 && ip4[1] == 255 && ip4[2] == 255
}

// PartitionAddrs splits a zone's answer set into genuine listing addresses and
// reserved 127.255.255.x error/status codes (see IsErrorCode).
func PartitionAddrs(addrs []string) ([]string, []string) {
	var listings, errCodes []string

	for _, addr := range addrs {
		if IsErrorCode(addr) {
			errCodes = append(errCodes, addr)
		} else {
			listings = append(listings, addr)
		}
	}

	return listings, errCodes
}

// IsNotFound reports whether err is an NXDOMAIN / host-not-found DNS error,
// which for a DNSBL means the target is NOT listed (clean).
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.IsNotFound
	}

	return false
}

// Result is the classification of one IP across a set of zones. The three zone
// lists are disjoint and sorted.
type Result struct {
	Listed       []string
	Clean        []string
	Inconclusive []string
}

// Query looks ip up on every zone, in parallel, and classifies each zone as
// listed, clean or inconclusive (a lookup failure, a timeout or a reserved
// 127.255.255.x reply). An IP that cannot be turned into a query form (neither
// IPv4 nor IPv6) leaves every zone inconclusive.
func Query(ctx context.Context, lookuper Lookuper, ip string, zones []string) Result {
	reversed := ReverseIP(ip)

	var (
		mu  sync.Mutex
		res Result
		wg  sync.WaitGroup
	)

	for _, zone := range zones {
		wg.Add(1)

		go func() {
			defer wg.Done()

			state := classify(ctx, lookuper, reversed, zone)

			mu.Lock()
			defer mu.Unlock()

			switch state {
			case stateListed:
				res.Listed = append(res.Listed, zone)
			case stateClean:
				res.Clean = append(res.Clean, zone)
			default:
				res.Inconclusive = append(res.Inconclusive, zone)
			}
		}()
	}

	wg.Wait()

	slices.Sort(res.Listed)
	slices.Sort(res.Clean)
	slices.Sort(res.Inconclusive)

	return res
}

type zoneState int

const (
	stateInconclusive zoneState = iota
	stateListed
	stateClean
)

func classify(ctx context.Context, lookuper Lookuper, reversed, zone string) zoneState {
	if reversed == "" {
		return stateInconclusive
	}

	addrs, err := lookuper.LookupHost(ctx, reversed+"."+zone)

	switch {
	case err == nil && len(addrs) > 0:
		if listings, _ := PartitionAddrs(addrs); len(listings) > 0 {
			return stateListed
		}

		return stateInconclusive
	case IsNotFound(err):
		return stateClean
	default:
		return stateInconclusive
	}
}
