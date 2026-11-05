import { describe, expect, it } from "vitest";
import { auditTarget } from "./audit-target";

describe("auditTarget", () => {
  it("names the incident's check and links to the incident for incident events", () => {
    // Incident lifecycle events carry no target_* keys: their payload names
    // the check, and the event row carries the incident and check UIDs.
    const event = {
      eventType: "incident.created",
      incidentUid: "0b5f-incident",
      checkUid: "9c1e-check",
      payload: { check_uid: "9c1e-check", check_slug: "api-acme", check_name: "API acme" },
    };
    expect(auditTarget(event)).toEqual({ label: "API acme", incidentUid: "0b5f-incident" });
  });

  it("falls back to the check slug, then the incident UID", () => {
    expect(auditTarget({ incidentUid: "i1", payload: { check_slug: "api-acme" } })).toEqual({
      label: "api-acme",
      incidentUid: "i1",
    });
    expect(auditTarget({ incidentUid: "i1", payload: {} })).toEqual({ label: "i1", incidentUid: "i1" });
  });

  it("keeps the explicit target of other events", () => {
    expect(
      auditTarget({ checkUid: "c1", payload: { target_name: "Acme API", target_type: "check" } }),
    ).toEqual({ label: "Acme API" });
    expect(auditTarget({ payload: { target_uid: "u1" } })).toEqual({ label: "u1" });
    expect(auditTarget({ payload: { target_type: "organization" } })).toEqual({ label: "organization" });
    expect(auditTarget({})).toEqual({ label: "—" });
  });
});
