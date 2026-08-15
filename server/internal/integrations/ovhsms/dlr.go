package ovhsms

import "strconv"

// DeliveryState is the coarse, provider-neutral outcome an OVH `ptt` code maps
// onto. The internal timeline only ever renders these three, so a Twilio and
// an OVH delivery read identically.
type DeliveryState string

// Delivery states.
const (
	// DeliveryPending means the message is still in flight: OVH is retrying,
	// or the operator has accepted it but not yet confirmed handset delivery.
	DeliveryPending DeliveryState = "pending"
	// DeliveryDelivered means the handset confirmed receipt.
	DeliveryDelivered DeliveryState = "delivered"
	// DeliveryFailed means the message will not be delivered.
	DeliveryFailed DeliveryState = "failed"
	// DeliveryUnknown means OVH itself cannot say (ptt 6), or the code is one
	// we have no mapping for. Deliberately distinct from failed: reporting a
	// delivered alert as failed is as misleading as the reverse.
	DeliveryUnknown DeliveryState = "unknown"
)

// pttDescriptions maps the OVH `ptt` delivery-report codes to their state and
// a short human description. Source: OVHcloud's own documentation repository
// (pages/web_cloud/messaging/sms/tout_savoir_sur_les_utilisateurs_sms). The
// list OVH publishes is explicitly non-exhaustive, which is why the default
// branch of PttState is "unknown" rather than "failed".
//
// Rebuilt per call rather than kept in a package var, to satisfy
// gochecknoglobals — the table is tiny and this is a callback path.
func pttDescriptions() map[int]struct {
	state DeliveryState
	desc  string
} {
	type entry = struct {
		state DeliveryState
		desc  string
	}

	return map[int]entry{
		1:   {DeliveryPending, "not yet delivered, retrying (handset)"},
		2:   {DeliveryPending, "not yet delivered, retrying (operator)"},
		3:   {DeliveryPending, "accepted by the operator"},
		4:   {DeliveryDelivered, "delivered"},
		5:   {DeliveryFailed, "confirmed undelivered, no further detail"},
		6:   {DeliveryUnknown, "operator cannot determine delivery"},
		8:   {DeliveryFailed, "expired in the operator SMSC"},
		20:  {DeliveryFailed, "undeliverable in its current form"},
		21:  {DeliveryFailed, "rejected: insufficient subscriber credit"},
		23:  {DeliveryFailed, "undeliverable: invalid or blacklisted number"},
		24:  {DeliveryFailed, "subscriber temporarily unavailable"},
		25:  {DeliveryFailed, "temporary network error"},
		26:  {DeliveryFailed, "temporary handset error (memory full)"},
		27:  {DeliveryFailed, "handset incompatible with this message type"},
		28:  {DeliveryFailed, "rejected as suspected spam"},
		29:  {DeliveryFailed, "prohibited content"},
		33:  {DeliveryFailed, "blocked by a parental lock"},
		39:  {DeliveryFailed, "operator failure"},
		73:  {DeliveryFailed, "ported number unreachable"},
		74:  {DeliveryFailed, "failed: roaming MSISDN"},
		76:  {DeliveryFailed, "ported number blocked for this client"},
		202: {DeliveryFailed, "ported number blocked; contact OVH support"},
	}
}

// PttState maps a raw `ptt` query-parameter value from an OVH delivery-receipt
// callback to a state and a short description.
//
// The input is attacker-influenceable — the callback endpoint is unsigned — so
// this never trusts the value beyond parsing it as an integer, and an
// unparseable or unmapped code yields DeliveryUnknown with a description that
// echoes nothing back.
func PttState(raw string) (DeliveryState, string) {
	code, err := strconv.Atoi(raw)
	if err != nil {
		return DeliveryUnknown, "unrecognized delivery code"
	}

	if entry, ok := pttDescriptions()[code]; ok {
		return entry.state, entry.desc
	}

	return DeliveryUnknown, "unmapped delivery code " + strconv.Itoa(code)
}
