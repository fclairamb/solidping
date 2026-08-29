import { Card, CardContent } from "@/components/ui/card";
import { JsonAssertionResults, type AssertionResult } from "./json-assertion-results";

type Output = Record<string, unknown>;

// The output key JsonAssertionResultCard renders itself. Callers that also
// dump the raw output (e.g. the result-detail page) strip this so it isn't
// shown twice — mirrors DNSBL_OUTPUT_KEYS in dnsbl-card.tsx.
export const JSON_ASSERTION_RESULT_OUTPUT_KEY = "json_path_assertions";

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
    typeof candidate.type === "string" &&
    typeof candidate.pass === "boolean"
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
