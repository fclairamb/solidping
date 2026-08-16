import { useState, useEffect, useRef } from "react";
import { apiFetch } from "@/api/client";

export interface FieldError {
  name: string;
  message: string;
}

interface ValidateResponse {
  valid: boolean;
  fields?: FieldError[];
  /** Advisory, per-field notes on an otherwise VALID config — e.g. pinning
   * `ipVersion: ipv6` in a region whose live workers report no IPv6 egress
   * (spec 2026-08-15-11). Never blocks: the check is created and runs, because
   * the run-time egress probe is the authority and the advertised capability
   * can lag. */
  warnings?: FieldError[];
}

export interface CheckValidationResult {
  errors: FieldError[];
  warnings: FieldError[];
}

/** Full validation result, errors and advisory warnings alike. */
export function useCheckValidationResult(
  org: string,
  type: string | undefined,
  config: Record<string, unknown>,
  regions: string[] = [],
  debounceMs = 1000
): CheckValidationResult {
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
            body: JSON.stringify({ type, config, regions }),
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
  }, [org, type, JSON.stringify(config), JSON.stringify(regions), debounceMs]);

  return { errors, warnings };
}

/** Errors only — the long-standing shape, kept for call sites that do not care
 * about advisory warnings. */
export function useCheckValidation(
  org: string,
  type: string | undefined,
  config: Record<string, unknown>,
  regions: string[] = [],
  debounceMs = 1000
): FieldError[] {
  return useCheckValidationResult(org, type, config, regions, debounceMs).errors;
}

export function getFieldError(
  errors: FieldError[],
  name: string
): string | undefined {
  return errors.find((e) => e.name === name)?.message;
}
