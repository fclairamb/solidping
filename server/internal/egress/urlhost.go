package egress

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// ErrInvalidIPv4Host is returned by ParseURLHost for a host that a browser
// would treat as an IPv4 address (it "ends in a number") but that is not a
// valid one. A browser rejects such a URL; callers must not treat it as a
// domain name either.
var ErrInvalidIPv4Host = errors.New("host ends in a number but is not a valid IPv4 address")

const (
	maxIPv4Parts   = 4
	ipv4PartMax    = 255
	bitsPerIPv4Oct = 8
)

// ParseURLHost interprets a URL host the way a browser does (WHATWG URL
// standard, "host parser" + "IPv4 parser"), for the one question the egress
// policy asks: does this host already name an address, and which one?
//
// Go's net.ParseIP only knows the dotted-quad form, but a browser also reads
// 2130706433, 0x7f000001, 0177.0.0.1 and 127.1 as 127.0.0.1. A check that
// judged those as domain names would ask DNS, get "no such host", let the URL
// through, and Chrome would then connect to loopback.
//
// It returns the IP for an IPv6 literal (bracketed or not) or any IPv4 form,
// (nil, nil) for a domain name, and ErrInvalidIPv4Host for a host that ends
// in a number but does not parse as IPv4.
func ParseURLHost(host string) (net.IP, error) {
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")

	if strings.Contains(host, ":") {
		if ip := net.ParseIP(host); ip != nil {
			return ip, nil
		}

		return nil, fmt.Errorf("%w: %q", ErrInvalidIPv4Host, host)
	}

	parts := strings.Split(strings.ToLower(host), ".")

	// A single trailing dot is allowed ("127.0.0.1." is 127.0.0.1).
	if len(parts) > 1 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}

	if !endsInNumber(parts) {
		return nil, nil
	}

	if len(parts) > maxIPv4Parts {
		return nil, fmt.Errorf("%w: %q", ErrInvalidIPv4Host, host)
	}

	numbers := make([]uint64, 0, len(parts))

	for _, part := range parts {
		n, ok := parseIPv4Number(part)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrInvalidIPv4Host, host)
		}

		numbers = append(numbers, n)
	}

	last := len(numbers) - 1

	for _, n := range numbers[:last] {
		if n > ipv4PartMax {
			return nil, fmt.Errorf("%w: %q", ErrInvalidIPv4Host, host)
		}
	}

	// The last part fills every byte the earlier parts left: 127.1 is
	// 127.0.0.1, 2130706433 is the whole address.
	if numbers[last] >= 1<<(bitsPerIPv4Oct*(maxIPv4Parts-last)) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidIPv4Host, host)
	}

	value := numbers[last]
	for i, n := range numbers[:last] {
		value += n << (bitsPerIPv4Oct * (maxIPv4Parts - 1 - i))
	}

	return net.IPv4(byte(value>>24), byte(value>>16), byte(value>>8), byte(value)).To4(), nil
}

// endsInNumber is the WHATWG "ends in a number checker": the last label is
// all ASCII digits, or parses as an IPv4 number (hex, octal).
func endsInNumber(parts []string) bool {
	lastPart := parts[len(parts)-1]
	if lastPart == "" {
		return false
	}

	if strings.Trim(lastPart, "0123456789") == "" {
		return true
	}

	_, ok := parseIPv4Number(lastPart)

	return ok
}

// parseIPv4Number is the WHATWG "IPv4 number parser": 0x/0X prefix is hex
// (an empty remainder is 0), a leading 0 is octal, anything else decimal.
func parseIPv4Number(part string) (uint64, bool) {
	if part == "" {
		return 0, false
	}

	base := 10

	switch {
	case strings.HasPrefix(part, "0x"):
		part, base = part[2:], 16
	case len(part) > 1 && part[0] == '0':
		part, base = part[1:], 8
	}

	if part == "" {
		return 0, true
	}

	n, err := strconv.ParseUint(part, base, 64)
	if err != nil {
		return 0, false
	}

	return n, true
}
