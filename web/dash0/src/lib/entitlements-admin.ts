import type { EntitlementsLimits } from "@/api/hooks";

/**
 * The numeric caps the superadmin editor exposes, in the order it renders
 * them. Order matters: it is also the order the confirmation dialog lists a
 * diff in, and a stable order is what makes two saves comparable at a glance.
 */
export const ADMIN_LIMIT_KEYS = [
  "maxChecks",
  "maxChecksPerMinute",
  "maxUsers",
  "maxDeportedAgents",
  "maxCustomDomains",
  "maxSlos",
  "maxSmsPerMonth",
  "maxCallsPerMonth",
  "maxWhatsappPerMonth",
] as const;

export type AdminLimitKey = (typeof ADMIN_LIMIT_KEYS)[number];

/**
 * One numeric cap as the form holds it while the operator types.
 *
 * `unlimited` is a first-class flag rather than "the field is empty", because
 * on this API **null means unlimited** — an empty text box that silently saved
 * as null would hand an organization an unbounded plan by accident. The two
 * states have to be distinguishable on screen, so they are distinguishable in
 * the model too.
 */
export interface LimitField {
  unlimited: boolean;
  value: string;
}

/** Builds a form field from a resolved cap. null/undefined = unlimited. */
export function limitFieldFrom(value?: number | null): LimitField {
  if (value === null || value === undefined) {
    return { unlimited: true, value: "" };
  }

  return { unlimited: false, value: String(value) };
}

/**
 * Whether a field can be saved: either it is unlimited, or it holds a
 * non-negative integer. Zero is valid and meaningful — an org suspended to
 * 0 checks/min is a real state, not an empty box.
 */
export function isLimitFieldValid(field: LimitField): boolean {
  if (field.unlimited) {
    return true;
  }

  const trimmed = field.value.trim();
  if (trimmed === "") {
    return false;
  }

  return /^\d+$/.test(trimmed);
}

/**
 * The wire value for a field: null when unlimited, the parsed integer
 * otherwise. Invalid input yields null only because callers are expected to
 * have gated on {@link isLimitFieldValid} first — never call this on a form
 * that has not been validated.
 */
export function limitFieldToValue(field: LimitField): number | null {
  if (field.unlimited || !isLimitFieldValid(field)) {
    return null;
  }

  return Number.parseInt(field.value.trim(), 10);
}

/** Builds the whole limits payload from the form's fields. */
export function limitsFromFields(
  fields: Record<AdminLimitKey, LimitField>,
  whiteLabel: WhiteLabelChoice,
): EntitlementsLimits {
  const out: EntitlementsLimits = {};

  for (const key of ADMIN_LIMIT_KEYS) {
    out[key] = limitFieldToValue(fields[key]);
  }

  out.whiteLabel = whiteLabelToValue(whiteLabel);

  return out;
}

/** Builds the form's fields from a limits payload. */
export function fieldsFromLimits(
  limits: EntitlementsLimits | undefined,
): Record<AdminLimitKey, LimitField> {
  const out = {} as Record<AdminLimitKey, LimitField>;

  for (const key of ADMIN_LIMIT_KEYS) {
    out[key] = limitFieldFrom(limits?.[key]);
  }

  return out;
}

/**
 * Renders a cap for display. `null`/`undefined` is **unlimited**, which is why
 * the caller must pass a translated word for it: an empty cell, a dash or a
 * "0" would all read as the opposite of what the value means.
 */
export function formatLimit(
  value: number | null | undefined,
  unlimitedLabel: string,
): string {
  if (value === null || value === undefined) {
    return unlimitedLabel;
  }

  return String(value);
}

/**
 * White-label is the one non-numeric entitlement, and its null does NOT mean
 * unlimited — it means "use the deployment default". Three states, so the
 * editor has to offer three choices rather than a checkbox.
 */
export type WhiteLabelChoice = "default" | "allowed" | "denied";

export function whiteLabelFrom(value?: boolean | null): WhiteLabelChoice {
  if (value === null || value === undefined) {
    return "default";
  }

  return value ? "allowed" : "denied";
}

export function whiteLabelToValue(choice: WhiteLabelChoice): boolean | null {
  if (choice === "default") {
    return null;
  }

  return choice === "allowed";
}

/**
 * Where an organization's limits came from, as a discriminated union the UI
 * turns into "Free defaults" / "Billing: Pro" / "Admin override since …".
 *
 * `since` is only meaningful for a stored row — an org on defaults has no
 * "since", and inventing one (the org creation date, say) would be a lie
 * dressed as precision.
 */
export type Provenance =
  | { kind: "default" }
  | { kind: "billing"; planName?: string | null; since?: string }
  | { kind: "admin"; since?: string }
  | { kind: "orgAdmin"; since?: string }
  | { kind: "other"; source: string };

export function provenanceOf(input: {
  source?: string;
  displayName?: string | null;
  stored?: { source?: string; updatedAt?: string } | null;
}): Provenance {
  const source = input.stored?.source ?? input.source;

  if (source === "admin") {
    return { kind: "admin", since: input.stored?.updatedAt };
  }

  // `org-admin` is a real stored row written through the org-scoped route. It
  // is NOT an override — billing still corrects it — but it is emphatically
  // not "free defaults" either, and showing it as such would tell an operator
  // the org is unconfigured when somebody configured it.
  if (source === "org-admin") {
    return { kind: "orgAdmin", since: input.stored?.updatedAt };
  }

  if (source === "billing-service") {
    return {
      kind: "billing",
      planName: input.displayName,
      since: input.stored?.updatedAt,
    };
  }

  if (source === undefined || source === "default" || source === "self-hosted") {
    return { kind: "default" };
  }

  return { kind: "other", source };
}

/** One changed cap, for the save confirmation. */
export interface LimitChange {
  key: AdminLimitKey | "whiteLabel";
  from: number | boolean | null;
  to: number | boolean | null;
}

/**
 * The caps that would actually change if this form were saved.
 *
 * This is what makes the confirmation dialog worth showing: restating "you are
 * about to save entitlements" tells an operator nothing, whereas "maxChecks
 * 100 → unlimited" is the sentence they can catch a mistake in.
 */
export function limitsDiff(
  before: EntitlementsLimits | undefined,
  after: EntitlementsLimits,
): LimitChange[] {
  const changes: LimitChange[] = [];

  for (const key of ADMIN_LIMIT_KEYS) {
    const from = normalizeLimit(before?.[key]);
    const to = normalizeLimit(after[key]);

    if (from !== to) {
      changes.push({ key, from, to });
    }
  }

  const whiteLabelBefore = normalizeFlag(before?.whiteLabel);
  const whiteLabelAfter = normalizeFlag(after.whiteLabel);

  if (whiteLabelBefore !== whiteLabelAfter) {
    changes.push({
      key: "whiteLabel",
      from: whiteLabelBefore,
      to: whiteLabelAfter,
    });
  }

  return changes;
}

function normalizeLimit(value: number | null | undefined): number | null {
  return value === undefined ? null : value;
}

function normalizeFlag(value: boolean | null | undefined): boolean | null {
  return value === undefined ? null : value;
}
