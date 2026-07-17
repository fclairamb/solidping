// Tunnel-edge helpers shared by the check-detail components. Kept out of the
// component file so fast-refresh stays happy (components-only exports there).
import type { Check } from "@/api/hooks";

// TUNNEL_CHECK_UID_KEY is the well-known config key referencing the SSH check a
// probe is dialed through. It mirrors the backend constant of the same name.
export const TUNNEL_CHECK_UID_KEY = "tunnelCheckUid";

/** Reads the tunnel reference off a check's config, or undefined when direct. */
export function tunnelCheckUidOf(check: Check | undefined): string | undefined {
  const value = check?.config?.[TUNNEL_CHECK_UID_KEY];
  return typeof value === "string" && value !== "" ? value : undefined;
}

/** The best human label for a check: name, else slug, else uid. */
export function checkLabel(check: Check): string {
  return check.name || check.slug || check.uid;
}
