import { useState, useEffect, useRef } from "react";
import { apiFetch } from "@/api/client";

/** How a finding should be treated. Absent means `"error"` — the field was
 * added by spec 2026-08-26-05 to a shape that predates it, and a producer that
 * omits it is always reporting a blocking error. */
export type FieldSeverity = "error" | "warning" | "info";

export interface FieldError {
  name: string;
  message: string;
  severity?: FieldSeverity;
  /** Stable machine identifier for the rule behind the finding — the half a
   * client may branch on, since messages get reworded. See
   * {@link VALIDATION_CODES}. */
  code?: string;
}

/** The finding codes this UI actually branches on. */
export const VALIDATION_CODES = {
  /** The slug is already taken by another live check of the org. */
  slugTaken: "SLUG_TAKEN",
  /** The proposed period/regions would put the org over its
   * `maxChecksPerMinute` cap. Advisory: it renders the link to the check
   * scheduling page, and never blocks the save. */
  orgRateOverLimit: "ORG_RATE_OVER_LIMIT",
} as const;

interface ValidateResponse {
  valid: boolean;
  fields?: FieldError[];
  /** Advisory, per-field notes on an otherwise VALID config — e.g. pinning
   * `ipVersion: ipv6` in a region whose live workers report no IPv6 egress
   * (spec 2026-08-15-11), or a schedule that would put the org over its
   * per-minute execution cap (spec 2026-08-26-05). Never blocks: the check is
   * created and runs, because the run-time egress probe is the authority and
   * the advertised capability can lag, and because an over-limit org has to be
   * able to edit its way back under the cap. */
  warnings?: FieldError[];
}

export interface CheckValidationResult {
  errors: FieldError[];
  warnings: FieldError[];
}

/** The not-yet-saved check shape sent to the validate endpoint. Everything
 * beyond type/config is optional; each field unlocks the rules that need it
 * (slug uniqueness, the period bounds, the org-rate projection). */
export interface CheckValidationInput {
  type: string | undefined;
  config: Record<string, unknown>;
  regions?: string[];
  /** Proposed slug, checked for format and for uniqueness within the org. */
  slug?: string;
  /** "HH:MM:SS". Omit for a passive check, or while no period is chosen. */
  period?: string;
  /** Defaults to enabled server-side; a disabled check costs no rate budget. */
  enabled?: boolean;
  /** UID of the check being edited: stops its own slug reading as a collision,
   * and makes the rate projection replace its stored row rather than add one. */
  excludeCheckUid?: string;
}

/** Full validation result, errors and advisory warnings alike. */
export function useCheckValidationResult(
  org: string,
  input: CheckValidationInput,
  debounceMs = 1000
): CheckValidationResult {
  const {
    type,
    config,
    regions = [],
    slug,
    period,
    enabled,
    excludeCheckUid,
  } = input;
  const [errors, setErrors] = useState<FieldError[]>([]);
  const [warnings, setWarnings] = useState<FieldError[]>([]);
  const isFirstRender = useRef(true);
  const timerRef = useRef<ReturnType<typeof setTimeout>>(undefined);

  useEffect(() => {
    if (isFirstRender.current) {
      isFirstRender.current = false;
      return;
    }

    if (!type) return;

    // Skip if config is empty (no meaningful fields set)
    const hasValues = Object.values(config).some(
      (v) => v !== undefined && v !== null && v !== ""
    );
    if (!hasValues) {
      setErrors([]);
      setWarnings([]);
      return;
    }

    if (timerRef.current) {
      clearTimeout(timerRef.current);
    }

    timerRef.current = setTimeout(async () => {
      try {
        const resp = await apiFetch<ValidateResponse>(
          `/api/v1/orgs/${org}/checks/validate`,
          {
            method: "POST",
            body: JSON.stringify({
              type,
              config,
              regions,
              ...(period ? { period } : {}),
              ...(enabled === undefined ? {} : { enabled }),
              ...(excludeCheckUid ? { excludeCheckUid } : {}),
              ...(slug ? { slug } : {}),
            }),
          }
        );

        setErrors(resp.valid ? [] : (resp.fields ?? []));
        setWarnings(resp.warnings ?? []);
      } catch {
        // Silently ignore validation errors (network issues, etc.)
        setErrors([]);
        setWarnings([]);
      }
    }, debounceMs);

    return () => {
      if (timerRef.current) {
        clearTimeout(timerRef.current);
      }
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    org,
    type,
    JSON.stringify(config),
    JSON.stringify(regions),
    period,
    enabled,
    excludeCheckUid,
    slug,
    debounceMs,
  ]);

  return { errors, warnings };
}

/** Errors only — the long-standing shape, kept for call sites that do not care
 * about advisory warnings. */
export function useCheckValidation(
  org: string,
  input: CheckValidationInput,
  debounceMs = 1000
): FieldError[] {
  return useCheckValidationResult(org, input, debounceMs).errors;
}

export function getFieldError(
  errors: FieldError[],
  name: string
): string | undefined {
  return errors.find((e) => e.name === name)?.message;
}

/** The first finding carrying `code`, or undefined. Branching on the code
 * rather than on the field name is what lets one field carry several distinct
 * findings (e.g. `period` holds both a bounds error and the org-rate warning). */
export function findFinding(
  findings: FieldError[],
  code: string
): FieldError | undefined {
  return findings.find((f) => f.code === code);
}
