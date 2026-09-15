package checkudp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	sampleTimeout = 5 * time.Second

	// SampleDNSQuery is a DNS query for `solidping.io A` with transaction ID
	// 0x5350, written as hex groups: header (ID, flags RD, QDCOUNT=1, the three
	// zero counts), then QNAME `9 solidping 2 io 0`, QTYPE A, QCLASS IN.
	SampleDNSQuery = "5350 0100 0001 0000 0000 0000 09 736f6c696470696e67 02 696f 00 0001 0001"
	// SampleDNSExpect is our transaction ID echoed back with QR/RD/RA set and
	// RCODE 0 — i.e. the resolver actually answered OUR question, which a bare
	// port probe can never establish.
	SampleDNSExpect = "53508180"

	// SampleNTPRequest is the 48-byte NTP client request: LI=0, VN=3, Mode=3
	// (0x1b) followed by 47 zero bytes, written as twelve 4-byte words so the
	// length is checkable by eye.
	SampleNTPRequest = "1b000000 00000000 00000000 00000000 00000000 00000000 " +
		"00000000 00000000 00000000 00000000 00000000 00000000"
	// SampleNTPExpect matches a server reply (Mode 4) at version 3 (0x1c) or
	// version 4 (0x24). The leap-indicator bits live in the same byte, so this
	// is deliberately a two-value class rather than an equality.
	SampleNTPExpect = `^[\x1c\x24]`
)

// GetSampleConfigs returns sample UDP check configurations.
//
// Every sample SENDS something and ASSERTS the answer: a UDP dial that sends
// nothing cannot fail short of an ICMP port-unreachable, so a silent sample
// would teach the tautological form of the check.
func (c *UDPChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Google DNS (53)",
			Slug:   "udp-google-dns",
			Period: time.Minute * 5,
			Config: (&UDPConfig{
				Host:           "8.8.8.8",
				Port:           53,
				Timeout:        sampleTimeout,
				SendData:       SampleDNSQuery,
				SendEncoding:   checkerdef.PayloadEncodingHex,
				ExpectData:     SampleDNSExpect,
				ExpectEncoding: checkerdef.PayloadEncodingHex,
			}).GetConfig(),
		},
		{
			Name:   "NTP Pool (123)",
			Slug:   "udp-ntp-pool",
			Period: time.Minute * 5,
			Config: (&UDPConfig{
				Host:          "pool.ntp.org",
				Port:          123,
				Timeout:       sampleTimeout,
				SendData:      SampleNTPRequest,
				SendEncoding:  checkerdef.PayloadEncodingHex,
				ExpectPattern: SampleNTPExpect,
			}).GetConfig(),
		},
	}
}
