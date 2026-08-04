package whatsapp

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Delivery status values Meta reports on the `statuses[]` array.
const (
	StatusSent      = "sent"
	StatusDelivered = "delivered"
	StatusRead      = "read"
	StatusFailed    = "failed"
)

// WebhookPayload is the subset of Meta's webhook envelope we consume. Unknown
// fields are ignored by encoding/json, so Meta adding keys never breaks us.
type WebhookPayload struct {
	Object string         `json:"object"`
	Entry  []WebhookEntry `json:"entry"`
}

// WebhookEntry is one WABA's worth of changes.
type WebhookEntry struct {
	ID      string          `json:"id"`
	Changes []WebhookChange `json:"changes"`
}

// WebhookChange is one field change (we only care about field == "messages").
type WebhookChange struct {
	Field string       `json:"field"`
	Value WebhookValue `json:"value"`
}

// WebhookValue carries the delivery statuses and inbound messages.
type WebhookValue struct {
	//nolint:tagliatelle // Meta's webhook wire format uses snake_case.
	MessagingProduct string           `json:"messaging_product"`
	Metadata         WebhookMetadata  `json:"metadata"`
	Statuses         []MessageStatus  `json:"statuses"`
	Messages         []InboundMessage `json:"messages"`
}

// WebhookMetadata identifies the receiving business number.
type WebhookMetadata struct {
	//nolint:tagliatelle // Meta's webhook wire format uses snake_case.
	DisplayPhoneNumber string `json:"display_phone_number"`
	//nolint:tagliatelle // Meta's webhook wire format uses snake_case.
	PhoneNumberID string `json:"phone_number_id"`
}

// MessageStatus is one delivery-status transition for a message we sent.
type MessageStatus struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
	//nolint:tagliatelle // Meta's webhook wire format uses snake_case.
	RecipientID  string         `json:"recipient_id"`
	Errors       []StatusError  `json:"errors"`
	Conversation map[string]any `json:"conversation,omitempty"`
}

// StatusError is Meta's per-status error detail (present on `failed`).
type StatusError struct {
	Code    int    `json:"code"`
	Title   string `json:"title"`
	Message string `json:"message"`
	//nolint:tagliatelle // Meta's webhook wire format uses snake_case.
	ErrorData struct {
		Details string `json:"details"`
	} `json:"error_data"`
}

// InboundMessage is a user reply. v1 only logs these: replies open the free
// 24-hour session window but carry no commands.
type InboundMessage struct {
	From      string `json:"from"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Text      struct {
		Body string `json:"body"`
	} `json:"text"`
}

// ParseWebhook decodes a webhook body. The caller must have validated the
// signature over these exact bytes first.
func ParseWebhook(body []byte) (*WebhookPayload, error) {
	var payload WebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse whatsapp webhook: %w", err)
	}

	return &payload, nil
}

// Statuses flattens every delivery status across all entries and changes.
func (p *WebhookPayload) Statuses() []MessageStatus {
	if p == nil {
		return nil
	}

	var out []MessageStatus

	for i := range p.Entry {
		for j := range p.Entry[i].Changes {
			out = append(out, p.Entry[i].Changes[j].Value.Statuses...)
		}
	}

	return out
}

// InboundMessages flattens every inbound user message.
func (p *WebhookPayload) InboundMessages() []InboundMessage {
	if p == nil {
		return nil
	}

	var out []InboundMessage

	for i := range p.Entry {
		for j := range p.Entry[i].Changes {
			out = append(out, p.Entry[i].Changes[j].Value.Messages...)
		}
	}

	return out
}

// Describe renders a short, storable summary of a delivery status for the
// notification audit row — "whatsapp status: failed (131026 Message
// undeliverable: recipient is not a WhatsApp user)". Never carries credentials.
func (s *MessageStatus) Describe() string {
	var builder strings.Builder

	builder.WriteString("whatsapp status: ")
	builder.WriteString(s.Status)

	for i := range s.Errors {
		statusErr := &s.Errors[i]

		fmt.Fprintf(&builder, " (%d %s", statusErr.Code, statusErr.Title)

		detail := statusErr.ErrorData.Details
		if detail == "" {
			detail = statusErr.Message
		}

		if detail != "" {
			builder.WriteString(": ")
			builder.WriteString(detail)
		}

		builder.WriteString(")")
	}

	return builder.String()
}
