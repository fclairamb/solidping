import { Card, CardContent } from "@/components/ui/card";
import {
  JsonAssertionResults,
  type AssertionResult,
} from "./json-assertion-results";

type Output = Record<string, unknown>;

// The output key JsonAssertionResultCard renders itself. Callers that also
// dump the raw output (e.g. the result-detail page) strip this so it isn't
// shown twice — mirrors DNSBL_OUTPUT_KEYS in dnsbl-card.tsx.
export const JSON_ASSERTION_RESULT_OUTPUT_KEY = "json_path_assertions";

// The output key BodyAssertionResultCard renders. Same treatment: callers that
// also dump the raw output strip it so it isn't shown twice.
export const BODY_ASSERTION_RESULT_OUTPUT_KEY = "body_assertions";

// isAssertionResult narrows an unknown output value to the AssertionResult
// shape the checker's Evaluate() emits (checkhttp/jsonpath.go). checker.go
// only attaches this key to Output when the assertion FAILS — a passing
// assertion adds nothing, so this card never mounts on a successful result.
// That's the evaluator's existing contract, not a gap this spec closes.
function isAssertionResult(value: unknown): value is AssertionResult {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return false;
  }
  const candidate = value as Record<string, unknown>;
  return (
    typeof candidate.type === "string" && typeof candidate.pass === "boolean"
  );
}

// JsonAssertionResultCard renders the JSONPath assertion evaluation attached
// to a failed HTTP check result. Returns null when the output carries no
// (well-formed) assertion result, so it is safe to mount unconditionally.
// JsonAssertionResults renders its own "JSON Assertions" heading + pass/fail
// badge, so this card adds no redundant CardTitle of its own.
export function JsonAssertionResultCard({
  output,
}: {
  output: Output | undefined;
}) {
  const result = output?.[JSON_ASSERTION_RESULT_OUTPUT_KEY];
  if (!isAssertionResult(result)) return null;

  return (
    <Card data-testid="json-assertion-result-card">
      <CardContent className="pt-6">
        <JsonAssertionResults result={result} />
      </CardContent>
    </Card>
  );
}

// BodyAssertionResultCard renders the plain-text body-assertion evaluation
// attached to a failed HTTP check result. Like the JSONPath card it mounts
// only on a failure (the checker attaches the tree only when the assertion
// fails) and returns null otherwise, so it is safe to mount unconditionally.
//
// The tree it renders carries `expected` AND `actual`, which is the whole
// point: a check that went down with nothing but "assertion failed" is not
// debuggable.
export function BodyAssertionResultCard({
  output,
}: {
  output: Output | undefined;
}) {
  const result = output?.[BODY_ASSERTION_RESULT_OUTPUT_KEY];
  if (!isAssertionResult(result)) return null;

  return (
    <Card data-testid="body-assertion-result-card">
      <CardContent className="pt-6">
        <JsonAssertionResults result={result} titleKey="bodyAssertions" />
      </CardContent>
    </Card>
  );
}
