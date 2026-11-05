import type { Event } from "@/api/hooks";

export interface AuditTarget {
  /** What the audit log's Target column shows. */
  label: string;
  /** Set for incident events: the row links to that incident. */
  incidentUid?: string;
}

function str(value: unknown): string | undefined {
  return typeof value === "string" && value !== "" ? value : undefined;
}

/**
 * auditTarget pulls the human-readable target out of the redacted payload the
 * backend wrote (target_name / target_uid). Incident lifecycle events write no
 * target_* keys: they name the incident's check (check_name / check_slug) and
 * carry the incident UID on the event itself, so they show the check and link
 * to the incident. Events that name no specific object fall back to the target
 * type alone.
 */
export function auditTarget(event: Event): AuditTarget {
  const payload = event.payload ?? {};
  const explicit = str(payload.target_name) ?? str(payload.target_uid);
  if (explicit) return { label: explicit };

  const incidentUid = str(event.incidentUid);
  if (incidentUid) {
    return {
      label: str(payload.check_name) ?? str(payload.check_slug) ?? incidentUid,
      incidentUid,
    };
  }

  return { label: str(payload.target_type) ?? "—" };
}
