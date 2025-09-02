import { useState, useEffect, useRef } from "react";
import { apiFetch } from "@/api/client";

export interface FieldError {
  name: string;
  message: string;
}

interface ValidateResponse {
  valid: boolean;
  fields?: FieldError[];
}

export function useCheckValidation(
  org: string,
  type: string | undefined,
  config: Record<string, unknown>,
  debounceMs = 1000
): FieldError[] {
  const [errors, setErrors] = useState<FieldError[]>([]);
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
            body: JSON.stringify({ type, config }),
          }
        );

        setErrors(resp.valid ? [] : (resp.fields ?? []));
      } catch {
        // Silently ignore validation errors (network issues, etc.)
        setErrors([]);
      }
    }, debounceMs);

    return () => {
      if (timerRef.current) {
        clearTimeout(timerRef.current);
      }
    };
  }, [org, type, JSON.stringify(config), debounceMs]);

  return errors;
}

export function getFieldError(
  errors: FieldError[],
  name: string
): string | undefined {
  return errors.find((e) => e.name === name)?.message;
}
