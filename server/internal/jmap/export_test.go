package jmap

import "context"

// SyncEmailsForTest exposes the internal syncEmails loop for unit tests.
func (m *Manager) SyncEmailsForTest(
	ctx context.Context, client *Client, mboxes *Mailboxes, cfg *Config,
) error {
	return m.syncEmails(ctx, client, mboxes, cfg)
}

// RecordErrorForTest exposes recordError for unit tests.
func (m *Manager) RecordErrorForTest(err error) {
	m.recordError(err)
}

// ExpandEventSourceURLForTest exposes the unexported helper for unit tests.
//
//nolint:gochecknoglobals // test-only export.
var ExpandEventSourceURLForTest = expandEventSourceURL
