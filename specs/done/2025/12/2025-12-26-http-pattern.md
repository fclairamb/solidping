# HTTP Pattern Matching Implementation

## Overview
Add pattern matching capabilities to HTTP checks to validate or invalidate responses based on body content and response headers.

## Configuration Parameters

### Body Pattern Matching
- `body_expect` (string): Simple string match. Check passes if string is found in response body.
- `body_reject` (string): Simple string match. Check fails if string is found in response body.
- `body_pattern` (string): Regular expression. Check passes if pattern matches in response body.
- `body_pattern_reject` (string): Regular expression. Check fails if pattern matches in response body.

### Header Pattern Matching
- `headers_pattern` (map[string]string): Map of header names to regex patterns. Check passes if all specified headers match their patterns.

## Implementation Details

### 1. Configuration Validation
- Body parameters are mutually compatible - multiple can be specified
- All patterns must be valid regex expressions
- Header names in `headers_pattern` should be case-insensitive
- Empty patterns should be rejected during configuration validation

### 2. Execution Order
Pattern matching should occur after:
1. HTTP request completes successfully (status code received)
2. Response body is fully read
3. Response headers are available

Pattern matching should occur before:
1. Status code validation
2. Certificate validation results

This ensures pattern matching can override status code checks if needed.

### 3. Body Pattern Matching Logic

#### Simple String Matching (`body_expect`, `body_reject`)
```
1. Read complete response body
2. If body_expect is set:
   - Search for exact string match (case-sensitive)
   - If NOT found: FAIL with error "Expected string not found in response body"
3. If body_reject is set:
   - Search for exact string match (case-sensitive)
   - If found: FAIL with error "Rejected string found in response body"
4. Continue to regex pattern matching
```

#### Regex Pattern Matching (`body_pattern`, `body_pattern_reject`)
```
1. If body_pattern is set:
   - Compile regex pattern
   - Match against response body
   - If NOT matched: FAIL with error "Expected pattern not found in response body"
2. If body_pattern_reject is set:
   - Compile regex pattern
   - Match against response body
   - If matched: FAIL with error "Rejected pattern found in response body"
```

**Regex Compilation:**
- Compile regex patterns once during configuration loading
- Cache compiled patterns in check configuration
- Return validation error if pattern is invalid regex

**Pattern Matching Mode:**
- Use `FindString` or `MatchString` for Go regex
- Patterns match anywhere in the body (not just full string match)
- Use `(?m)` for multiline mode if needed in pattern

### 4. Header Pattern Matching Logic

```
1. For each entry in headers_pattern:
   - Get header value from response (case-insensitive header name lookup)
   - If header is missing: FAIL with error "Required header '<name>' not found"
   - Compile regex pattern (or use cached)
   - Match pattern against header value
   - If NOT matched: FAIL with error "Header '<name>' value does not match pattern"
2. If all headers match: PASS
```

**Header Name Matching:**
- HTTP headers are case-insensitive per RFC 7230
- Use `http.Header.Get()` which handles case-insensitivity
- Pattern matching is case-sensitive on header values

**Multi-value Headers:**
- If header has multiple values, concatenate with ", " (comma-space) per HTTP spec
- Or match against any single value (implementation choice)

### 5. Overall Check Result

The check fails if ANY of these conditions are true:
1. `body_expect` string is not found
2. `body_reject` string is found
3. `body_pattern` regex does not match
4. `body_pattern_reject` regex matches
5. Any header in `headers_pattern` is missing or doesn't match

The check passes if ALL validation rules pass.

### 6. Error Messages

Error messages should be descriptive and include:
- Which validation failed
- The pattern/string that was being matched
- Optionally: excerpt of actual content (truncated if too long)

Examples:
```
"Expected string 'Status: OK' not found in response body"
"Rejected string 'Error:' found in response body"
"Expected pattern 'version: v\d+\.\d+' not found in response body"
"Rejected pattern 'Exception|Error|Fatal' found in response body"
"Required header 'Content-Type' not found"
"Header 'X-API-Version' value 'v1.0' does not match pattern 'v2\.\d+'"
```

### 7. Performance Considerations

- Read response body only once
- Compile regex patterns during configuration load, not during each check
- Set reasonable body size limits (e.g., 10MB) to prevent memory issues
- Consider streaming for large bodies if needed

### 8. Implementation Steps

1. **Update HTTP check configuration struct:**
   - Add new fields for pattern matching parameters
   - Add validation in config loading

2. **Compile regex patterns:**
   - During configuration validation/loading
   - Store `*regexp.Regexp` in config struct
   - Return error if invalid regex

3. **Implement pattern matching functions:**
   - `validateBodyExpect(body string, expect string) error`
   - `validateBodyReject(body string, reject string) error`
   - `validateBodyPattern(body string, pattern *regexp.Regexp) error`
   - `validateBodyPatternReject(body string, pattern *regexp.Regexp) error`
   - `validateHeadersPattern(headers http.Header, patterns map[string]*regexp.Regexp) error`

4. **Integrate into HTTP checker:**
   - After successful HTTP request
   - Before returning check result
   - Aggregate all validation errors

5. **Add tests:**
   - Unit tests for each validation function
   - Integration tests with real HTTP responses
   - Edge cases: empty body, missing headers, invalid regex, etc.

## Examples

### Example 1: Simple String Matching
```yaml
type: http
url: https://api.example.com/health
body_expect: "healthy"
body_reject: "error"
```

### Example 2: Regex Pattern Matching
```yaml
type: http
url: https://api.example.com/version
body_pattern: "version: v\\d+\\.\\d+\\.\\d+"
body_pattern_reject: "(error|exception|fatal)"
```

### Example 3: Header Validation
```yaml
type: http
url: https://api.example.com/data
headers_pattern:
  Content-Type: "application/json"
  X-API-Version: "v[2-9]\\.\\d+"
  Cache-Control: "public, max-age=\\d+"
```

### Example 4: Combined Validation
```yaml
type: http
url: https://api.example.com/status
body_expect: "OK"
body_pattern_reject: "(?i)(error|warning|fail)"
headers_pattern:
  Content-Type: "application/json"
  X-Request-ID: ".+"
```

## Edge Cases

1. **Empty response body:**
   - `body_expect`: Should fail (string not found)
   - `body_reject`: Should pass (string not found)
   - `body_pattern`: Should fail (pattern not matched)
   - `body_pattern_reject`: Should pass (pattern not matched)

2. **Missing header:**
   - Should fail with clear error message

3. **Case sensitivity:**
   - Header names: case-insensitive
   - Header values: case-sensitive (unless pattern uses `(?i)`)
   - Body matching: case-sensitive (unless pattern uses `(?i)`)

4. **Binary response bodies:**
   - Convert to string for matching
   - May contain invalid UTF-8 - handle gracefully

5. **Very large response bodies:**
   - Set reasonable size limits
   - Consider truncating or streaming

6. **Regex compilation errors:**
   - Catch during configuration validation
   - Provide clear error message with pattern

## Testing Requirements

1. **Unit tests:**
   - Each validation function with various inputs
   - Regex compilation and matching
   - Header case-insensitivity

2. **Integration tests:**
   - Full HTTP check with pattern matching
   - Real HTTP server responses
   - All combinations of parameters

3. **Error cases:**
   - Invalid regex patterns
   - Missing headers
   - Pattern mismatches
   - Empty bodies

4. **Performance tests:**
   - Large response bodies
   - Complex regex patterns
   - Many header validations
