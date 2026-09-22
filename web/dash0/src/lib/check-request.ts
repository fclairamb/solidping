import type { CreateCheckRequest, UpdateCheckRequest } from "@/api/hooks";
import type { CheckFormData } from "@/components/shared/check-form";

/**
 * The form's payload keys that are NOT part of a create/update request body.
 *
 * This is a DENY-list on purpose, and that is the whole point of this module.
 * Both check routes used to hand-pick the fields they forwarded, each carrying a
 * comment warning that "a field added to the form but missed here is silently
 * dropped before the request is ever sent". The warning did not work: it was
 * written after spec 2026-07-15-04 lost `confirmationPeriodSeconds` that way, and
 * spec 2026-09-22-03's six degraded-detection fields were lost the same way
 * immediately afterwards — the form collected them, the section rendered, Save
 * reported success, and the PATCH body never carried them.
 *
 * With a deny-list the failure mode inverts: a new form field reaches the server
 * by default, and the only way to drop one is to name it here deliberately.
 *
 * The three entries below are applied through separate endpoints after the check
 * itself is written: channel bindings via `PUT …/channels`, and dependency edges
 * via the check-dependency mutations (`initialDependsOn` exists only so the
 * caller can diff).
 */
const NON_REQUEST_KEYS = [
  "connectionUids",
  "dependsOn",
  "initialDependsOn",
] as const;

/**
 * `type` is create-only: a check's type is immutable once it exists (the edit
 * form renders it as a disabled input), and `UpdateCheckRequest` has no such
 * field. The form still carries it in its payload, so the update direction drops
 * it here rather than relying on `check-form.tsx` happening to leave it
 * `undefined` on edit and JSON.stringify happening to omit that.
 */
const UPDATE_ONLY_EXCLUDED_KEYS = ["type"] as const;

/** Strips the named keys, leaving the request body. */
function requestFields(
  data: CheckFormData,
  alsoExclude: readonly string[] = [],
): Record<string, unknown> {
  const out: Record<string, unknown> = { ...data };

  for (const key of [...NON_REQUEST_KEYS, ...alsoExclude]) {
    delete out[key];
  }

  return out;
}

/**
 * toUpdateCheckRequest builds the PATCH body from the form's payload.
 *
 * A key whose value is `undefined` is dropped by JSON.stringify, which is
 * precisely the PATCH semantics the routes relied on when they wrote
 * `regionSpread: data.regionSpread` behind a conditional — so passing it through
 * unconditionally is behaviour-preserving, not a widening.
 */
export function toUpdateCheckRequest(data: CheckFormData): UpdateCheckRequest {
  return requestFields(data, UPDATE_ONLY_EXCLUDED_KEYS) as UpdateCheckRequest;
}

/**
 * toCreateCheckRequest builds the POST body from the same payload.
 *
 * Two create-only normalizations, both carried over from the route this replaces:
 * `config` must be an object (the type requires it), and an EMPTY
 * escalation-policy / traceroute value means "not chosen" on create rather than
 * "clear it", so those are omitted instead of sent blank.
 */
export function toCreateCheckRequest(data: CheckFormData): CreateCheckRequest {
  const out = requestFields(data);

  out.config = data.config ?? {};

  if (!data.escalationPolicyUid) delete out.escalationPolicyUid;
  if (!data.tracerouteOnFailure) delete out.tracerouteOnFailure;

  return out as unknown as CreateCheckRequest;
}
