package jmap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Errors returned by typed method wrappers.
var (
	ErrMailboxNotCreated = errors.New("jmap: mailbox/set did not create the requested mailbox")
	ErrMailboxNotFound   = errors.New("jmap: mailbox not found")
	ErrEmptyResponse     = errors.New("jmap: empty method response")
	ErrParseResponse     = errors.New("jmap: parse method response")
	ErrPartialUpdate     = errors.New("jmap: not all emails updated")
	ErrPartialDestroy    = errors.New("jmap: not all emails destroyed")
)

// JSON keys reused across calls.
const (
	keyAccountID  = "accountId"
	clientIDFirst = "c0"
)

// defaultEmailProperties is the property list requested by EmailGet for the
// inbox manager. It avoids attachment bodies and HTML content — only envelope
// metadata + a short preview.
//
//nolint:gochecknoglobals // immutable property list shared across calls
var defaultEmailProperties = []string{
	"id", "blobId", "threadId", "mailboxIds",
	"from", "to", "cc", "bcc", "replyTo",
	"subject", "messageId", "inReplyTo", "references",
	"receivedAt", "sentAt", "preview", "headers", "attachments", "size",
}

// DefaultEmailProperties returns a copy of the default Email/get property list.
func DefaultEmailProperties() []string {
	out := make([]string, len(defaultEmailProperties))
	copy(out, defaultEmailProperties)

	return out
}

// MailboxQuery returns the IDs of every mailbox in the account.
func (c *Client) MailboxQuery(ctx context.Context, accountID string) ([]string, error) {
	resp, err := c.Call(ctx, []MethodCall{
		{
			Name:     "Mailbox/query",
			Args:     map[string]any{keyAccountID: accountID},
			ClientID: clientIDFirst,
		},
	})
	if err != nil {
		return nil, err
	}

	if len(resp.MethodResponses) == 0 {
		return nil, fmt.Errorf("%w: Mailbox/query", ErrEmptyResponse)
	}

	var out struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(resp.MethodResponses[0].Args, &out); err != nil {
		return nil, fmt.Errorf("%w: Mailbox/query: %w", ErrParseResponse, err)
	}

	return out.IDs, nil
}

// MailboxGet fetches mailbox details by ID. If ids is empty, returns all
// mailboxes for the account.
func (c *Client) MailboxGet(ctx context.Context, accountID string, ids []string) ([]Mailbox, error) {
	args := map[string]any{keyAccountID: accountID}
	if len(ids) > 0 {
		args["ids"] = ids
	}

	resp, err := c.Call(ctx, []MethodCall{
		{Name: "Mailbox/get", Args: args, ClientID: clientIDFirst},
	})
	if err != nil {
		return nil, err
	}

	if len(resp.MethodResponses) == 0 {
		return nil, fmt.Errorf("%w: Mailbox/get", ErrEmptyResponse)
	}

	var out struct {
		List []Mailbox `json:"list"`
	}
	if err := json.Unmarshal(resp.MethodResponses[0].Args, &out); err != nil {
		return nil, fmt.Errorf("%w: Mailbox/get: %w", ErrParseResponse, err)
	}

	return out.List, nil
}

// FindMailboxByName returns the first mailbox matching name (exact, case-
// sensitive). When the mailbox does not exist, the returned error wraps
// ErrMailboxNotFound — callers should test with errors.Is.
func (c *Client) FindMailboxByName(ctx context.Context, accountID, name string) (*Mailbox, error) {
	mailboxes, err := c.MailboxGet(ctx, accountID, nil)
	if err != nil {
		return nil, err
	}

	for i := range mailboxes {
		if mailboxes[i].Name == name {
			return &mailboxes[i], nil
		}
	}

	return nil, fmt.Errorf("%w: name=%s", ErrMailboxNotFound, name)
}

// FindMailboxByRole returns the mailbox with the given JMAP role (e.g. "trash"
// or "inbox"). When no mailbox has the role, the returned error wraps
// ErrMailboxNotFound.
func (c *Client) FindMailboxByRole(ctx context.Context, accountID, role string) (*Mailbox, error) {
	mailboxes, err := c.MailboxGet(ctx, accountID, nil)
	if err != nil {
		return nil, err
	}

	for i := range mailboxes {
		if mailboxes[i].Role == role {
			return &mailboxes[i], nil
		}
	}

	return nil, fmt.Errorf("%w: role=%s", ErrMailboxNotFound, role)
}

// FindOrCreateMailbox returns the mailbox with the given name, creating it
// at the top level if it does not exist.
func (c *Client) FindOrCreateMailbox(ctx context.Context, accountID, name string) (*Mailbox, error) {
	existing, err := c.FindMailboxByName(ctx, accountID, name)
	if err == nil {
		return existing, nil
	}

	if !errors.Is(err, ErrMailboxNotFound) {
		return nil, err
	}

	tempID := "new"

	resp, err := c.Call(ctx, []MethodCall{
		{
			Name: "Mailbox/set",
			Args: map[string]any{
				keyAccountID: accountID,
				"create": map[string]any{
					tempID: map[string]any{"name": name},
				},
			},
			ClientID: clientIDFirst,
		},
	})
	if err != nil {
		return nil, err
	}

	if len(resp.MethodResponses) == 0 {
		return nil, fmt.Errorf("%w: Mailbox/set", ErrEmptyResponse)
	}

	var out struct {
		Created map[string]Mailbox `json:"created"`
	}
	if err := json.Unmarshal(resp.MethodResponses[0].Args, &out); err != nil {
		return nil, fmt.Errorf("%w: Mailbox/set: %w", ErrParseResponse, err)
	}

	created, ok := out.Created[tempID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrMailboxNotCreated, name)
	}

	created.Name = name

	return &created, nil
}

// EmailQueryFilter is the subset of JMAP Email filter conditions used by the
// inbox manager.
type EmailQueryFilter struct {
	InMailbox    string
	Before       string // ISO-8601 timestamp; emails received strictly before
	NotInMailbox string
}

// EmailQuery returns email IDs matching the filter.
func (c *Client) EmailQuery(ctx context.Context, accountID string, filter EmailQueryFilter) ([]string, error) {
	cond := map[string]any{}
	if filter.InMailbox != "" {
		cond["inMailbox"] = filter.InMailbox
	}

	if filter.NotInMailbox != "" {
		cond["notInMailbox"] = filter.NotInMailbox
	}

	if filter.Before != "" {
		cond["before"] = filter.Before
	}

	args := map[string]any{
		keyAccountID: accountID,
		"sort": []map[string]any{
			{"property": "receivedAt", "isAscending": false},
		},
	}
	if len(cond) > 0 {
		args["filter"] = cond
	}

	resp, err := c.Call(ctx, []MethodCall{
		{Name: "Email/query", Args: args, ClientID: clientIDFirst},
	})
	if err != nil {
		return nil, err
	}

	if len(resp.MethodResponses) == 0 {
		return nil, fmt.Errorf("%w: Email/query", ErrEmptyResponse)
	}

	var out struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(resp.MethodResponses[0].Args, &out); err != nil {
		return nil, fmt.Errorf("%w: Email/query: %w", ErrParseResponse, err)
	}

	return out.IDs, nil
}

// EmailGet fetches the listed email IDs with the given properties. If
// properties is nil, the default property list is used.
func (c *Client) EmailGet(
	ctx context.Context, accountID string, ids, properties []string,
) ([]Email, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	if properties == nil {
		properties = defaultEmailProperties
	}

	args := map[string]any{
		keyAccountID: accountID,
		"ids":        ids,
		"properties": properties,
	}

	resp, err := c.Call(ctx, []MethodCall{
		{Name: "Email/get", Args: args, ClientID: clientIDFirst},
	})
	if err != nil {
		return nil, err
	}

	if len(resp.MethodResponses) == 0 {
		return nil, fmt.Errorf("%w: Email/get", ErrEmptyResponse)
	}

	var out struct {
		List []Email `json:"list"`
	}
	if err := json.Unmarshal(resp.MethodResponses[0].Args, &out); err != nil {
		return nil, fmt.Errorf("%w: Email/get: %w", ErrParseResponse, err)
	}

	return out.List, nil
}

// EmailSetMailbox moves the given emails from one mailbox to another. JMAP
// expresses this as an `update` with a `mailboxIds/<from>` removal and a
// `mailboxIds/<to>` addition (RFC 8621 §4.6 patch semantics).
func (c *Client) EmailSetMailbox(
	ctx context.Context, accountID string, ids []string, fromMailboxID, toMailboxID string,
) error {
	if len(ids) == 0 {
		return nil
	}

	updates := make(map[string]any, len(ids))

	for _, id := range ids {
		patch := map[string]any{
			"mailboxIds/" + toMailboxID: true,
		}
		if fromMailboxID != "" && fromMailboxID != toMailboxID {
			patch["mailboxIds/"+fromMailboxID] = nil
		}

		updates[id] = patch
	}

	resp, err := c.Call(ctx, []MethodCall{
		{
			Name: "Email/set",
			Args: map[string]any{
				keyAccountID: accountID,
				"update":     updates,
			},
			ClientID: clientIDFirst,
		},
	})
	if err != nil {
		return err
	}

	if len(resp.MethodResponses) == 0 {
		return fmt.Errorf("%w: Email/set update", ErrEmptyResponse)
	}

	var out struct {
		NotUpdated map[string]any `json:"notUpdated"`
	}
	if err := json.Unmarshal(resp.MethodResponses[0].Args, &out); err != nil {
		return fmt.Errorf("%w: Email/set update: %w", ErrParseResponse, err)
	}

	if len(out.NotUpdated) > 0 {
		return fmt.Errorf("%w: %d emails", ErrPartialUpdate, len(out.NotUpdated))
	}

	return nil
}

// EmailDestroy permanently deletes the listed email IDs.
func (c *Client) EmailDestroy(ctx context.Context, accountID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	resp, err := c.Call(ctx, []MethodCall{
		{
			Name: "Email/set",
			Args: map[string]any{
				keyAccountID: accountID,
				"destroy":    ids,
			},
			ClientID: clientIDFirst,
		},
	})
	if err != nil {
		return err
	}

	if len(resp.MethodResponses) == 0 {
		return fmt.Errorf("%w: Email/set destroy", ErrEmptyResponse)
	}

	var out struct {
		NotDestroyed map[string]any `json:"notDestroyed"`
	}
	if err := json.Unmarshal(resp.MethodResponses[0].Args, &out); err != nil {
		return fmt.Errorf("%w: Email/set destroy: %w", ErrParseResponse, err)
	}

	if len(out.NotDestroyed) > 0 {
		return fmt.Errorf("%w: %d emails", ErrPartialDestroy, len(out.NotDestroyed))
	}

	return nil
}

// EmailChanges returns the IDs created/updated/destroyed since the given
// state token. The caller iterates by calling again with each NewState until
// HasMoreChanges is false.
func (c *Client) EmailChanges(ctx context.Context, accountID, sinceState string) (*ChangesResponse, error) {
	resp, err := c.Call(ctx, []MethodCall{
		{
			Name: "Email/changes",
			Args: map[string]any{
				keyAccountID: accountID,
				"sinceState": sinceState,
			},
			ClientID: clientIDFirst,
		},
	})
	if err != nil {
		return nil, err
	}

	if len(resp.MethodResponses) == 0 {
		return nil, fmt.Errorf("%w: Email/changes", ErrEmptyResponse)
	}

	var out ChangesResponse
	if err := json.Unmarshal(resp.MethodResponses[0].Args, &out); err != nil {
		return nil, fmt.Errorf("%w: Email/changes: %w", ErrParseResponse, err)
	}

	return &out, nil
}
