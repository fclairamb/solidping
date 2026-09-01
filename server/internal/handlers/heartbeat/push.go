package heartbeat

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkheartbeat"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/heartbeatpush"
)

// ErrBeatRejected is the ONE error every push-transport rejection returns.
//
// It is deliberately undifferentiated. A caller must not be able to tell an
// unknown organization from an unknown check from a wrong token from a bad MAC
// from a replayed counter — otherwise the listener becomes an oracle that
// enumerates which checks exist and confirms when a guessed token is close.
// The reason is recorded in the server's own debug log and in the per-outcome
// Prometheus counters, never on the wire.
var ErrBeatRejected = errors.New("beat rejected")

// HandleBeat verifies and records one beat that arrived over an embedded push
// transport (transport is "tcp" or "udp"). remoteAddr is the source address,
// kept for forensics only — a UDP source address is spoofable and is never
// treated as identity.
//
// It returns accepted=false with a nil error for every rejection the caller
// must answer with silence, and a non-nil error only for an internal fault
// worth alerting on (a database failure). Both look identical on the wire.
//
// Verification order for SP2, which is also the order of increasing cost:
//
//  1. resolve the check (one indexed lookup),
//  2. recompute and compare the MAC in constant time,
//  3. bound `ts` against server time when the device has a clock,
//  4. advance the replay counter, which must be strictly greater.
//
// Doing the MAC before the counter matters: the counter advance is a WRITE,
// and an unauthenticated caller must never be able to move a check's counter
// forward (which would lock the real device out until it rebooted).
func (s *Service) HandleBeat(
	ctx context.Context, beat *heartbeatpush.Beat, remoteAddr, transport string,
) (bool, error) {
	if beat == nil {
		return false, nil
	}

	org, check, resolveErr := s.resolveCheck(ctx, beat.Org, beat.Identifier)
	if resolveErr != nil {
		// Deliberately swallowed: an unknown org and an unknown check must be
		// indistinguishable from a bad MAC on the wire, and neither is a
		// server fault worth surfacing as an error.
		s.logBeatRejection(ctx, beat, transport, "unknown target")

		return false, nil //nolint:nilerr // a rejection is not an internal fault
	}

	token, _ := check.Config["token"].(string)
	if token == "" {
		s.logBeatRejection(ctx, beat, transport, "check has no token")

		return false, nil
	}

	requireHMAC, cfgErr := checkheartbeat.RequireHMACFromConfig(check.Config)
	if cfgErr != nil {
		// A config that cannot be read is treated as STRICT, never as
		// permissive: the safe direction for an unreadable security setting is
		// to refuse the weaker message form.
		requireHMAC = true
	}

	if ok, verifyErr := s.verifyBeat(ctx, beat, check, token, requireHMAC, transport); !ok || verifyErr != nil {
		return false, verifyErr
	}

	if recErr := s.recordBeat(ctx, org, check, &Request{
		RemoteAddr: remoteAddr,
		Transport:  transport,
		Annotation: beat.Annotation,
	}); recErr != nil {
		return false, recErr
	}

	return true, nil
}

// verifyBeat runs the per-version credential checks. It returns ok=false for a
// rejection and a non-nil error only for an internal fault.
func (s *Service) verifyBeat(
	ctx context.Context, beat *heartbeatpush.Beat, check *models.Check,
	token string, requireHMAC bool, transport string,
) (bool, error) {
	if beat.Version == 1 {
		if requireHMAC {
			s.logBeatRejection(ctx, beat, transport, "unsigned beat on a require_hmac check")

			return false, nil
		}

		if subtle.ConstantTimeCompare([]byte(beat.Token), []byte(token)) != 1 {
			s.logBeatRejection(ctx, beat, transport, "token mismatch")

			return false, nil
		}

		return true, nil
	}

	if !heartbeatpush.VerifyMAC(token, beat.Signed, beat.MAC) {
		s.logBeatRejection(ctx, beat, transport, "mac mismatch")

		return false, nil
	}

	// ts == 0 means "this device has no clock" and skips the freshness check
	// entirely. The timestamp's only job is bounding damage if the server's
	// counter state is ever lost (a restored backup); the counter below is the
	// actual replay protection.
	if beat.Timestamp != 0 {
		window := s.timestampWindow()
		if delta := time.Since(time.Unix(beat.Timestamp, 0)); delta > window || delta < -window {
			s.logBeatRejection(ctx, beat, transport, "timestamp outside the freshness window")

			return false, nil
		}
	}

	advanced, err := s.db.TryAdvanceHeartbeatCounter(ctx, check.UID, beat.Counter)
	if err != nil {
		return false, err
	}

	if !advanced {
		s.logBeatRejection(ctx, beat, transport, "counter not strictly greater (replay)")

		return false, nil
	}

	return true, nil
}

// logBeatRejection records why a beat was dropped, at DEBUG.
//
// It logs the target and the reason and NOTHING else: never the token, never
// the MAC, never the raw annotation bytes. A rejection log is written on
// unauthenticated input, so anything echoed into it is attacker-controlled.
func (s *Service) logBeatRejection(ctx context.Context, beat *heartbeatpush.Beat, transport, reason string) {
	slog.DebugContext(ctx, "Heartbeat push beat rejected",
		"transport", transport,
		"protocol_version", beat.Version,
		"org", beat.Org,
		"identifier", beat.Identifier,
		"reason", reason,
	)
}
