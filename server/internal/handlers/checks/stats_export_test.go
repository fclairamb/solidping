package checks

import "time"

// SetCheckStatsTTLForTest overrides the per-org stats cache TTL so tests can
// exercise the staleness contract without sleeping for a minute. Lives in an
// _test.go file, so it is compiled into the package's test binary only and is
// never part of the shipped API.
func SetCheckStatsTTLForTest(s *Service, ttl time.Duration) {
	s.checkStatsTTL = ttl
}
