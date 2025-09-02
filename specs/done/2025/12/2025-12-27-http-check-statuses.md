# HTTP Check Custom Status Codes

## Problem

Currently, HTTP checks only validate against a single expected status code (typically `200`). This is too restrictive for real-world scenarios where:
- APIs may return `201 Created` for successful POST requests
- Redirects (`301`, `302`, `307`, `308`) may be acceptable responses
- Any `2XX` success code should be considered valid

## Proposed Solution

Allow HTTP checks to be configured with flexible status code matching:

1. **Exact codes**: `["200"]`, `["201"]`, `["404"]`
2. **Wildcard patterns**: `["2XX"]` (any 200-299), `["3XX"]` (any 300-399)
3. **Multiple values**: `["200", "201"]` or `["2XX", "3XX"]`

## Implementation

### Database Schema

Add a new field to the check configuration:
- Field: `expected_status_codes`
- Type: `[]string` (string array)
- Default: `["2XX"]` (matches current behavior of accepting any success code)

### Matching Logic

```go
// Example matching logic
// patterns is the expected_status_codes []string from config
func matchStatus(actual int, patterns []string) bool {
    for _, pattern := range patterns {
        if strings.HasSuffix(pattern, "XX") {
            // Wildcard match: "2XX" matches 200-299
            prefix := pattern[0]
            if actual/100 == int(prefix-'0') {
                return true
            }
        } else {
            // Exact match
            if strconv.Itoa(actual) == pattern {
                return true
            }
        }
    }
    return false
}
```

### API

The `expected_status_codes` field should be exposed in the check API:
```json
{
  "type": "http",
  "url": "https://api.example.com/resource",
  "expected_status_codes": ["2XX", "3XX"]
}
```

## Examples

| Pattern | Matches |
|---------|---------|
| `["200"]` | Only 200 |
| `["2XX"]` | 200, 201, 202, ..., 299 |
| `["200", "201"]` | 200 or 201 |
| `["2XX", "3XX"]` | Any 2XX or 3XX status |
| `["200", "301", "302"]` | 200, 301, or 302 |
