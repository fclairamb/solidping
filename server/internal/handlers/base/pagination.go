package base

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
)

// ErrInvalidLimit is returned when a query string carries a limit value that
// can't be parsed as a positive integer. Handlers translate it into a 400
// Validation response.
var ErrInvalidLimit = errors.New("invalid limit parameter")

// ParsePageLimit reads the page-size query parameter from `query` and clamps
// it to the [1, max] range. The canonical name is `limit`; for endpoints that
// previously used `size`, we accept it as a deprecated alias when `limit` is
// absent. When both are present, `limit` wins.
//
// Returns:
//   - the clamped value when a valid one is provided
//   - `def` when neither key is set
//   - ErrInvalidLimit (wrapped with the offending value) when the value can't
//     be parsed as a positive integer
//
// Caller is responsible for translating ErrInvalidLimit into the appropriate
// HTTP response.
func ParsePageLimit(query url.Values, def, max int) (int, error) {
	raw := query.Get("limit")
	if raw == "" {
		// Deprecated alias retained for one release so external clients
		// (CLI / MCP / scripts hardcoded against `size`) keep working.
		// Drop after the next API revision; tracked via the spec's PR-2.
		raw = query.Get("size")
	}

	if raw == "" {
		return def, nil
	}

	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: %q is not an integer", ErrInvalidLimit, raw)
	}

	if n < 1 {
		return 0, fmt.Errorf("%w: %d must be >= 1", ErrInvalidLimit, n)
	}

	if max > 0 && n > max {
		n = max
	}

	return n, nil
}
