// Package jmap provides a minimal JMAP (RFC 8620 / 8621) client and a
// long-running inbox manager used by SolidPing to receive emails on arbitrary
// addresses. The package is deliberately small: only the subset of JMAP we need
// for inbox monitoring is implemented (session discovery, Email/query,
// Email/get, Email/set, Mailbox/query, EventSource).
package jmap

import (
	"encoding/json"
	"time"
)

// Capability identifiers from RFC 8620 §1.6 / 8621 §2.
const (
	CapabilityCore = "urn:ietf:params:jmap:core"
	CapabilityMail = "urn:ietf:params:jmap:mail"
)

// MailboxRoleTrash is the standardized role for the trash mailbox.
const MailboxRoleTrash = "trash"

// Config holds the credentials and tunables used by the inbox manager.
type Config struct {
	Enabled                bool   `json:"enabled"`
	SessionURL             string `json:"sessionUrl"`
	Username               string `json:"username"`
	Password               string `json:"password"`
	AddressDomain          string `json:"addressDomain"`
	MailboxName            string `json:"mailboxName"`
	ProcessedMailboxName   string `json:"processedMailboxName"`
	PollIntervalSeconds    int    `json:"pollIntervalSeconds"`
	ProcessedRetentionDays int    `json:"processedRetentionDays"`
	FailedRetentionDays    int    `json:"failedRetentionDays"`
	RewriteBaseURL         string `json:"rewriteBaseUrl"`
}

// Default values applied when the corresponding Config fields are unset.
const (
	DefaultMailboxName            = "Inbox"
	DefaultProcessedMailboxName   = "Processed"
	DefaultPollIntervalSeconds    = 900
	DefaultProcessedRetentionDays = 30
	DefaultFailedRetentionDays    = 7
)

// ApplyDefaults fills in missing fields with their defaults. It does not
// validate required fields (SessionURL, Username, Password, AddressDomain) —
// callers should check those before connecting.
func (c *Config) ApplyDefaults() {
	if c.MailboxName == "" {
		c.MailboxName = DefaultMailboxName
	}

	if c.ProcessedMailboxName == "" {
		c.ProcessedMailboxName = DefaultProcessedMailboxName
	}

	if c.PollIntervalSeconds <= 0 {
		c.PollIntervalSeconds = DefaultPollIntervalSeconds
	}

	if c.ProcessedRetentionDays <= 0 {
		c.ProcessedRetentionDays = DefaultProcessedRetentionDays
	}

	if c.FailedRetentionDays <= 0 {
		c.FailedRetentionDays = DefaultFailedRetentionDays
	}
}

// Account is one of the accounts a JMAP user has access to.
type Account struct {
	Name                string         `json:"name"`
	IsPersonal          bool           `json:"isPersonal"`
	IsReadOnly          bool           `json:"isReadOnly"`
	AccountCapabilities map[string]any `json:"accountCapabilities"`
}

// Session is the response of a JMAP session discovery request (RFC 8620 §2).
type Session struct {
	Capabilities      map[string]any     `json:"capabilities"`
	Accounts          map[string]Account `json:"accounts"`
	PrimaryAccounts   map[string]string  `json:"primaryAccounts"`
	APIURL            string             `json:"apiUrl"`
	DownloadURL       string             `json:"downloadUrl"`
	UploadURL         string             `json:"uploadUrl"`
	EventSourceURL    string             `json:"eventSourceUrl"`
	State             string             `json:"state"`
	Username          string             `json:"username"`
	WebSocketURL      string             `json:"webSocketUrl,omitempty"`
	URLAuthentication string             `json:"urlAuthentication,omitempty"`
}

// MethodCall is a single invocation in a JMAP request envelope. The wire
// format is a 3-tuple `[name, args, clientID]` (RFC 8620 §3.2).
type MethodCall struct {
	Name     string
	Args     map[string]any
	ClientID string
}

// MarshalJSON encodes the call as a JSON array.
func (m MethodCall) MarshalJSON() ([]byte, error) {
	return json.Marshal([3]any{m.Name, m.Args, m.ClientID})
}

// UnmarshalJSON decodes a JSON array of length 3 into the call.
func (m *MethodCall) UnmarshalJSON(data []byte) error {
	var raw [3]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	if err := json.Unmarshal(raw[0], &m.Name); err != nil {
		return err
	}

	args := map[string]any{}
	if err := json.Unmarshal(raw[1], &args); err != nil {
		return err
	}

	m.Args = args

	return json.Unmarshal(raw[2], &m.ClientID)
}

// Request is a JMAP request envelope (RFC 8620 §3.3).
type Request struct {
	Using       []string     `json:"using"`
	MethodCalls []MethodCall `json:"methodCalls"`
}

// Response is a JMAP response envelope.
type Response struct {
	MethodResponses []MethodResponse `json:"methodResponses"`
	SessionState    string           `json:"sessionState,omitempty"`
}

// MethodResponse is the wire format `[name, args, clientID]` for a method
// invocation reply.
type MethodResponse struct {
	Name     string
	Args     json.RawMessage
	ClientID string
}

// MarshalJSON encodes the response as a JSON array.
func (m MethodResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal([3]any{m.Name, m.Args, m.ClientID})
}

// UnmarshalJSON decodes a JSON array of length 3 into the response.
func (m *MethodResponse) UnmarshalJSON(data []byte) error {
	var raw [3]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	if err := json.Unmarshal(raw[0], &m.Name); err != nil {
		return err
	}

	m.Args = raw[1]

	return json.Unmarshal(raw[2], &m.ClientID)
}

// EmailAddress is a parsed RFC 5322 address as JMAP returns it (RFC 8621 §4.1).
type EmailAddress struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

// EmailHeader represents a single raw header.
type EmailHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Attachment metadata. Body content itself is not loaded by the inbox
// manager — only the envelope metadata.
type Attachment struct {
	PartID      string `json:"partId,omitempty"`
	BlobID      string `json:"blobId"`
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	Size        int    `json:"size"`
	Disposition string `json:"disposition,omitempty"`
}

// Mailbox is a JMAP Mailbox (RFC 8621 §2).
type Mailbox struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ParentID     string `json:"parentId,omitempty"`
	Role         string `json:"role,omitempty"`
	SortOrder    int    `json:"sortOrder,omitempty"`
	TotalEmails  int    `json:"totalEmails,omitempty"`
	UnreadEmails int    `json:"unreadEmails,omitempty"`
}

// Email is the JMAP Email object (RFC 8621 §4) — the subset the inbox
// manager needs.
type Email struct {
	ID          string          `json:"id"`
	BlobID      string          `json:"blobId,omitempty"`
	ThreadID    string          `json:"threadId,omitempty"`
	MailboxIDs  map[string]bool `json:"mailboxIds,omitempty"`
	From        []EmailAddress  `json:"from,omitempty"`
	To          []EmailAddress  `json:"to,omitempty"`
	Cc          []EmailAddress  `json:"cc,omitempty"`
	Bcc         []EmailAddress  `json:"bcc,omitempty"`
	ReplyTo     []EmailAddress  `json:"replyTo,omitempty"`
	Subject     string          `json:"subject,omitempty"`
	MessageID   []string        `json:"messageId,omitempty"`
	InReplyTo   []string        `json:"inReplyTo,omitempty"`
	References  []string        `json:"references,omitempty"`
	ReceivedAt  time.Time       `json:"receivedAt"`
	SentAt      *time.Time      `json:"sentAt,omitempty"`
	Headers     []EmailHeader   `json:"headers,omitempty"`
	Preview     string          `json:"preview,omitempty"`
	Attachments []Attachment    `json:"attachments,omitempty"`
	Size        int             `json:"size,omitempty"`
}

// ChangesResponse is the result of an Email/changes call.
type ChangesResponse struct {
	AccountID      string   `json:"accountId"`
	OldState       string   `json:"oldState"`
	NewState       string   `json:"newState"`
	HasMoreChanges bool     `json:"hasMoreChanges"`
	Created        []string `json:"created"`
	Updated        []string `json:"updated"`
	Destroyed      []string `json:"destroyed"`
}

// EventSourceEvent is a parsed Server-Sent Event from the JMAP eventSourceUrl
// (RFC 8620 §7.3). The inbox manager only cares about `state` events.
type EventSourceEvent struct {
	Type string
	Data string
}

// StateChange is the payload of an EventSource `state` event (RFC 8620 §7).
// It maps account ID to a per-type-state map.
type StateChange struct {
	Type    string                       `json:"@type"`
	Changed map[string]map[string]string `json:"changed"`
}
