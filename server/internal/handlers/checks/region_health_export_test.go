package checks

import "time"

// SetRegionHealthNowForTest overrides the clock RegionHealth reads "now"
// through, so tests can pin it and assert the exact liveness boundary
// (last_active_at == now - WorkerLivenessWindow) instead of racing a live
// wall clock. Lives in an _test.go file, so it is compiled into the
// package's test binary only and is never part of the shipped API.
func SetRegionHealthNowForTest(s *Service, now func() time.Time) {
	s.now = now
}
