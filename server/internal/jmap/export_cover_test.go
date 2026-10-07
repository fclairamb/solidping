package jmap

import "context"

// CleanupOldEmailsForTest exposes cleanupOldEmails for unit tests.
func (m *Manager) CleanupOldEmailsForTest(
	ctx context.Context, client *Client, mboxes *Mailboxes, cfg *Config,
) error {
	return m.cleanupOldEmails(ctx, client, mboxes, cfg)
}

// ExtractEmailStateForTest exposes extractEmailState for unit tests.
//
//nolint:gochecknoglobals // test-only export.
var ExtractEmailStateForTest = extractEmailState

// SleepOrDoneForTest exposes sleepOrDone for unit tests.
//
//nolint:gochecknoglobals // test-only export.
var SleepOrDoneForTest = sleepOrDone

// SetModeForTest exposes setMode for unit tests.
func (m *Manager) SetModeForTest(mode string) { m.setMode(mode) }

// SetConnectedForTest exposes setConnected for unit tests.
func (m *Manager) SetConnectedForTest(v bool) { m.setConnected(v) }
