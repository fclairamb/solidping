package base

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// SettleWindow is how long a row must have existed before a forward ("asc")
// listing returns it. Two transactions can commit out of created_at order, so
// a row stamped earlier can become visible after a later one that a consumer
// already read past. Holding rows back this long means a consumer sees a row
// at most this late, never skips one. It is a constant, not a setting.
const SettleWindow = 5 * time.Second

// ErrInvalidOrder is returned when the order query parameter is neither asc nor desc.
var ErrInvalidOrder = errors.New("invalid order parameter: must be asc or desc")

// ParseOrder reads the order query parameter. It reports true for "asc"
// (oldest first) and false for "desc" (the default, newest first).
func ParseOrder(query url.Values) (bool, error) {
	switch raw := query.Get("order"); raw {
	case "", "desc":
		return false, nil
	case "asc":
		return true, nil
	default:
		return false, fmt.Errorf("%w (got %q)", ErrInvalidOrder, raw)
	}
}

// cursorSeparator splits the two halves of an opaque cursor.
const cursorSeparator = "|"

// EncodeCursor builds the opaque keyset cursor for the last row on a page.
//
// It carries BOTH halves of the sort key (timestamp, uid) because the ordering
// is not unique on the timestamp alone: several rows can share one to the
// microsecond, and a timestamp-only cursor would either repeat or skip them
// at a page boundary.
func EncodeCursor(ts time.Time, uid string) string {
	raw := ts.UTC().Format(time.RFC3339Nano) + cursorSeparator + uid

	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses a cursor produced by EncodeCursor. Anything it cannot
// parse, including the legacy bare-UID cursors that used to be handed out,
// reports !ok and the caller starts from the first page. A malformed cursor is
// a client bug, not a reason to 500.
func DecodeCursor(cursor string) (time.Time, string, bool) {
	if cursor == "" {
		return time.Time{}, "", false
	}

	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", false
	}

	parts := strings.SplitN(string(decoded), cursorSeparator, 2)
	if len(parts) != 2 || parts[1] == "" {
		return time.Time{}, "", false
	}

	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", false
	}

	return ts, parts[1], true
}
