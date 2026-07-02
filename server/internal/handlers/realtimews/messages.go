package realtimews

import "github.com/fclairamb/solidping/server/internal/realtime"

// ProtocolVersion is announced in the `hello` message so clients can detect
// incompatible server upgrades and force a full reconnect.
const ProtocolVersion = 2

// Client -> server message types.
const (
	msgTypeAuth        = "auth"
	msgTypeSubscribe   = "subscribe"
	msgTypeUnsubscribe = "unsubscribe"
)

// Server -> client message types.
const (
	msgTypeHello      = "hello"
	msgTypeSubscribed = "subscribed"
	msgTypeUpdate     = "update"
	msgTypeResync     = "resync"
	msgTypeError      = "error"
)

// clientMessage is the envelope every client->server frame decodes into.
// Fields not relevant to the message's type are simply left zero.
type clientMessage struct {
	Type   string `json:"type"`
	Token  string `json:"token,omitempty"`
	Entity string `json:"entity,omitempty"`
	UID    string `json:"uid,omitempty"`
}

// helloMessage is sent once, right after authentication succeeds.
type helloMessage struct {
	Type     string `json:"type"`
	Protocol int    `json:"protocol"`
}

func newHello() helloMessage {
	return helloMessage{Type: msgTypeHello, Protocol: ProtocolVersion}
}

// subscribedMessage acknowledges a (possibly duplicate) subscribe.
type subscribedMessage struct {
	Type   string `json:"type"`
	Entity string `json:"entity"`
	UID    string `json:"uid,omitempty"`
}

func newSubscribed(scope realtime.Scope) subscribedMessage {
	return subscribedMessage{Type: msgTypeSubscribed, Entity: string(scope.Entity), UID: scope.UID}
}

// updateMessage announces a subscribed scope changed.
type updateMessage struct {
	Type   string   `json:"type"`
	Entity string   `json:"entity"`
	UID    string   `json:"uid,omitempty"`
	Kinds  []string `json:"kinds,omitempty"`
}

func newUpdate(update realtime.ScopedUpdate) updateMessage {
	kinds := make([]string, 0, len(update.Kinds))
	for _, k := range update.Kinds {
		kinds = append(kinds, string(k))
	}

	return updateMessage{
		Type: msgTypeUpdate, Entity: string(update.Scope.Entity), UID: update.Scope.UID, Kinds: kinds,
	}
}

// resyncMessage tells the client to invalidate every currently subscribed
// scope once (bus transport gap recovery).
type resyncMessage struct {
	Type string `json:"type"`
}

func newResync() resyncMessage {
	return resyncMessage{Type: msgTypeResync}
}

// errorMessage reports a per-message failure. The socket stays open — this
// is not a close condition. Entity/UID echo the offending scope when known.
type errorMessage struct {
	Type   string `json:"type"`
	Code   string `json:"code"`
	Title  string `json:"title"`
	Entity string `json:"entity,omitempty"`
	UID    string `json:"uid,omitempty"`
}

func newError(code, title, entity, uid string) errorMessage {
	return errorMessage{Type: msgTypeError, Code: code, Title: title, Entity: entity, UID: uid}
}
