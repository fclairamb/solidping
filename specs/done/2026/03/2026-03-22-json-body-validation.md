# JSON Body Validation (JSONPath)

## Overview

Add JSONPath-based response body validation for HTTP checks. Allows users to assert specific values in JSON API responses beyond simple string matching. Supported by Uptime Kuma (JSON Query), Gatus (JSONPath conditions), and Checkly (assertions).

## Goals

1. Extract values from JSON responses using JSONPath expressions
2. Assert extracted values match expected conditions
3. Integrate into existing HTTP check type (not a new check type)

---

## HTTP Check Settings Extension

Add a new field to `HTTPConfig` in `back/internal/checkers/checkhttp/config.go`:

```go
type HTTPConfig struct {
    // ... existing fields ...
    JSONPathAssertions *AssertionNode `json:"json_path_assertions,omitempty"`
}
```

No database migration needed — `config` is stored as JSONB in the `checks` table.

### AST Node Types

Every node has a `type` discriminator field. Three node types:

| Type | Fields | Description |
|------|--------|-------------|
| `assertion` | `path`, `operator`, `value` | Leaf node — evaluates a single JSONPath condition |
| `and` | `children` | All child nodes must pass |
| `or` | `children` | At least one child node must pass |

#### Go Types

Create `back/internal/checkers/checkhttp/jsonpath.go`:

```go
type AssertionNodeType string

const (
    NodeTypeAssertion AssertionNodeType = "assertion"
    NodeTypeAnd       AssertionNodeType = "and"
    NodeTypeOr        AssertionNodeType = "or"
)

type AssertionNode struct {
    Type     AssertionNodeType `json:"type"`
    // Leaf fields (type=assertion)
    Path     string            `json:"path,omitempty"`
    Operator string            `json:"operator,omitempty"`
    Value    string            `json:"value,omitempty"`
    // Group fields (type=and|or)
    Children []AssertionNode   `json:"children,omitempty"`
}

type AssertionResult struct {
    Type     AssertionNodeType  `json:"type"`
    Pass     bool               `json:"pass"`
    // Leaf fields
    Path     string             `json:"path,omitempty"`
    Operator string             `json:"operator,omitempty"`
    Expected string             `json:"expected,omitempty"`
    Actual   string             `json:"actual,omitempty"`
    Error    string             `json:"error,omitempty"`
    // Group fields
    Children []AssertionResult  `json:"children,omitempty"`
}

// Evaluate recursively evaluates the AST against parsed JSON data.
func (n *AssertionNode) Evaluate(data any) AssertionResult { ... }
```

### JSON Examples

Single assertion:

```json
{
  "json_path_assertions": {
    "type": "assertion",
    "path": "$.status",
    "operator": "eq",
    "value": "healthy"
  }
}
```

Multiple assertions with AND:

```json
{
  "json_path_assertions": {
    "type": "and",
    "children": [
      { "type": "assertion", "path": "$.status", "operator": "eq", "value": "healthy" },
      { "type": "assertion", "path": "$.data.count", "operator": "gte", "value": "1" }
    ]
  }
}
```

Nested AND/OR:

```json
{
  "json_path_assertions": {
    "type": "and",
    "children": [
      { "type": "assertion", "path": "$.status", "operator": "eq", "value": "healthy" },
      {
        "type": "or",
        "children": [
          { "type": "assertion", "path": "$.region", "operator": "eq", "value": "eu-west-1" },
          { "type": "assertion", "path": "$.region", "operator": "eq", "value": "us-east-1" }
        ]
      }
    ]
  }
}
```

### Operators

| Operator | Description | Value comparison |
|----------|-------------|-----------------|
| `eq` | Equals | String comparison |
| `neq` | Not equals | String comparison |
| `gt` | Greater than | Parse both as float64 |
| `gte` | Greater than or equal | Parse both as float64 |
| `lt` | Less than | Parse both as float64 |
| `lte` | Less than or equal | Parse both as float64 |
| `contains` | String contains | Substring match |
| `regex` | Regex match | Compile value as regex, match against actual |
| `exists` | Path exists | No value needed — pass if JSONPath resolves |
| `not_exists` | Path does not exist | No value needed — pass if JSONPath returns no result |

### Behavior

1. Parse response body as JSON (`json.Unmarshal` into `any`)
2. Evaluate the AST recursively from the root node:
   - `assertion` node: run JSONPath query, compare result using operator, return `AssertionResult`
   - `and` node: evaluate all children, pass if all pass
   - `or` node: evaluate all children, pass if at least one passes
3. Check fails if the root node evaluation fails
4. Store the full `AssertionResult` tree in `Output["json_path_assertions"]` for diagnostics

---

## Backend Implementation

### Files to modify

| File | Change |
|------|--------|
| `back/internal/checkers/checkhttp/config.go` | Add `JSONPathAssertions *AssertionNode` field to `HTTPConfig` |
| `back/internal/checkers/checkhttp/checker.go` | Call `Evaluate()` after existing body/header checks in `Execute()` |

### Files to create

| File | Purpose |
|------|---------|
| `back/internal/checkers/checkhttp/jsonpath.go` | `AssertionNode`, `AssertionResult`, `Evaluate()`, operator logic |
| `back/internal/checkers/checkhttp/jsonpath_test.go` | Table-driven tests for AST evaluation |

### JSONPath library

Use `github.com/ohler55/ojg` — it provides JSONPath via `jp.ParseString()` + `jp.Get()`, supports full JSONPath spec, and is actively maintained.

### Validation

Add to `HTTPChecker.Validate()` in `checker.go`:
- Validate node type is one of `assertion`, `and`, `or`
- For leaf nodes: `path` must be non-empty, `operator` must be valid
- For `exists`/`not_exists`: `value` must be empty
- For numeric operators (`gt`, `gte`, `lt`, `lte`): `value` must parse as float64
- For `regex`: `value` must compile as valid regex
- For group nodes: `children` must be non-empty
- Return `ConfigError{Parameter: "json_path_assertions"}` on failure

### Execution order

In `checker.go` `Execute()`, after the existing body checks (`BodyExpect`, `BodyReject`, `BodyPattern`, `BodyPatternReject`) and header checks:

```go
if config.JSONPathAssertions != nil {
    var jsonData any
    if err := json.Unmarshal(bodyBytes, &jsonData); err != nil {
        return Result{Status: StatusDown, Output: map[string]any{
            "error": "response body is not valid JSON",
        }}
    }
    result := config.JSONPathAssertions.Evaluate(jsonData)
    output["json_path_assertions"] = result
    if !result.Pass {
        return Result{Status: StatusDown, ...}
    }
}
```

---

## Dashboard UI

### Files to modify

| File | Change |
|------|--------|
| `apps/dash0/src/components/shared/check-form.tsx` | Add JSON assertions section for HTTP checks |
| `apps/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx` | Show assertion results in check detail |

### Files to create

| File | Purpose |
|------|---------|
| `apps/dash0/src/components/checks/json-assertion-editor.tsx` | Tree-based AST editor component |
| `apps/dash0/src/components/checks/json-assertion-results.tsx` | Assertion result tree display |

### Assertion Builder (`json-assertion-editor.tsx`)

Renders inside the HTTP check form, below existing fields. Uses Shadcn/ui components.

- **Tree-based visual editor** that mirrors the AST structure
- Each node renders as a row:
  - **Assertion node**: three inline fields — JSONPath `Input`, operator `Select` dropdown, value `Input`. Hide value field for `exists`/`not_exists`.
  - **AND/OR group node**: a labeled container (`Card`) with its children indented below (`pl-6`), and a dropdown button to append a child (assertion or group)
- Controls per node:
  - `Trash2` icon button to remove
  - For groups: toggle button to switch between AND/OR
- **Empty state**: a single "Add JSON assertion" `Button` that creates the first assertion node
- Serialize the tree to `config.json_path_assertions` on form submit
- On edit, deserialize from existing `config.json_path_assertions`

**Operator dropdown options**: `eq`, `neq`, `gt`, `gte`, `lt`, `lte`, `contains`, `regex`, `exists`, `not_exists`

**Test IDs**: `data-testid="json-assertion-editor"`, `data-testid="json-assertion-add"`

### Result Display (`json-assertion-results.tsx`)

Shown on the check detail page when `lastResult.output.json_path_assertions` exists.

- Render the `AssertionResult` tree with indentation
- Each leaf shows: path, operator, expected, actual, pass/fail badge
- Each group shows: AND/OR label, pass/fail badge, children indented
- Colors: green (`text-green-600`) for pass, red (`text-red-600`) for fail
- Placed in the "Last Result" card, after existing output display

### Translations

Add to `apps/dash0/src/locales/en/checks.json` and `fr/checks.json`:

```json
{
  "jsonAssertions": "JSON Assertions",
  "addJsonAssertion": "Add JSON assertion",
  "addGroup": "Add group",
  "jsonPath": "JSONPath",
  "operator": "Operator",
  "expectedValue": "Expected value",
  "actualValue": "Actual value",
  "passed": "Passed",
  "failed": "Failed"
}
```

---

## Tests

### Backend (`jsonpath_test.go`)

Table-driven tests covering:

| Test case | Description |
|-----------|-------------|
| Single assertion eq | `$.status` eq `"healthy"` against `{"status":"healthy"}` → pass |
| Single assertion neq | `$.status` neq `"down"` → pass |
| Numeric gt | `$.count` gt `"5"` against `{"count":10}` → pass |
| Numeric lt fail | `$.count` lt `"5"` against `{"count":10}` → fail |
| Contains | `$.message` contains `"ok"` against `{"message":"all ok"}` → pass |
| Regex | `$.version` regex `"^v\\d+\\.\\d+"` → pass |
| Exists | `$.data` exists against `{"data":null}` → pass (path exists) |
| Not exists | `$.missing` not_exists against `{}` → pass |
| AND group all pass | Two passing children → pass |
| AND group one fails | One failing child → fail |
| OR group one passes | One passing child → pass |
| OR group all fail | All failing children → fail |
| Nested AND/OR | Complex tree → expected result |
| Invalid JSON body | Non-JSON response → fail with error |
| Invalid JSONPath | Malformed path expression → fail with error |
| Missing path | Path returns no result for non-exists operators → fail |

### Dashboard (Playwright)

Add to existing check E2E tests:
- Create HTTP check with JSON assertions → verify assertions saved in config
- Edit existing check → add/remove assertions → verify update
- Check detail page → verify assertion results display when present

---

## Competitor Reference

- **Uptime Kuma**: JSON Query monitor type with JSONPath
- **Gatus**: `[BODY].path.to.field` conditions with JSONPath
- **Checkly**: Assertions on API response body with operators
