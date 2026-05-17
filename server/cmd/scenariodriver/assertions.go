package main

import (
	"context"
	"fmt"
	"time"
)

// assertWebhook blocks until a payload matching predicate arrives on channelID
// within the given timeout. Returns nil on success, or a descriptive error on
// timeout or context cancellation.
func assertWebhook(
	ctx context.Context,
	receiver *WebhookReceiver,
	channelID string,
	description string,
	predicate func(map[string]any) bool,
	timeout time.Duration,
) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := receiver.WaitForEvent(waitCtx, channelID, predicate); err != nil {
		if waitCtx.Err() != nil {
			return fmt.Errorf("no webhook received within %s: %s", timeout, description)
		}

		return fmt.Errorf("webhook wait error: %w", err)
	}

	return nil
}

// matchEventType returns a predicate that matches a payload with the given
// eventType field.
func matchEventType(eventType string) func(map[string]any) bool {
	return func(m map[string]any) bool {
		et, _ := m["eventType"].(string)
		return et == eventType
	}
}

// matchResolvedAtPresent returns a predicate that matches payloads where
// resolvedAt is non-empty/non-null.
func matchResolvedAtPresent() func(map[string]any) bool {
	return func(m map[string]any) bool {
		v := m["resolvedAt"]
		return v != nil && v != ""
	}
}

// buildAssertPredicate combines the assertions from an expect_webhook step into
// a single predicate function.
func buildAssertPredicate(assert map[string]any) func(map[string]any) bool {
	return func(payload map[string]any) bool {
		for key, expected := range assert {
			switch key {
			case "event_type":
				et, _ := payload["eventType"].(string)
				if et != expected {
					return false
				}
			case "resolved_at_present":
				want, ok := expected.(bool)
				if !ok {
					continue
				}

				v := payload["resolvedAt"]
				hasResolved := v != nil && v != ""

				if want != hasResolved {
					return false
				}
			default:
				// Generic field equality check.
				if payload[key] != expected {
					return false
				}
			}
		}

		return true
	}
}

// describeAssert returns a human-readable description of what is being asserted.
func describeAssert(assert map[string]any) string {
	if et, ok := assert["event_type"].(string); ok {
		return et
	}

	return fmt.Sprintf("%v", assert)
}
