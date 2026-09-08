import { describe, expect, it } from "vitest";

import { connectionBindingsChanged } from "./connection-bindings";

// Spec 2026-09-07-02 §C.1. The edit page PUT the channel bindings on every
// save because the form always carries a `connectionUids` array in edit mode.
// For an ordinary user that was a wasted no-op write; for a demo session it
// was a 403 DEMO_READ_ONLY that took the whole (successful) check edit with
// it. This comparison is what makes the write conditional.
describe("connectionBindingsChanged", () => {
  it("says nothing changed when the same uids come back in a different order", () => {
    // The picker's selection order is not meaningful and the API returns
    // bindings in its own order, so an order-sensitive compare would report a
    // change on every save and defeat the entire fix.
    expect(connectionBindingsChanged(["a", "b"], ["b", "a"])).toBe(false);
    expect(connectionBindingsChanged(["a", "b", "c"], ["c", "a", "b"])).toBe(false);
  });

  it("says nothing changed for the demo's empty-to-empty save", () => {
    // The exact case in the bug report: a demo-created check has no bindings,
    // the picker is not even rendered, and the form still submits [].
    expect(connectionBindingsChanged([], [])).toBe(false);
  });

  it("detects an added, a removed and a swapped binding", () => {
    expect(connectionBindingsChanged(["a", "b"], ["a"])).toBe(true);
    expect(connectionBindingsChanged(["a"], ["a", "b"])).toBe(true);
    expect(connectionBindingsChanged(["a"], ["b"])).toBe(true);
    expect(connectionBindingsChanged(["a"], [])).toBe(true);
    expect(connectionBindingsChanged([], ["a"])).toBe(true);
  });

  it("ignores duplicates, which the replace endpoint collapses anyway", () => {
    expect(connectionBindingsChanged(["a", "a"], ["a"])).toBe(false);
    expect(connectionBindingsChanged(["a"], ["a", "a"])).toBe(false);
    expect(connectionBindingsChanged(["a", "a", "b"], ["b", "a"])).toBe(false);
  });

  it("does not write when the form submitted no selection at all", () => {
    // `undefined` means the form left the field out (create-mode shapes, or a
    // payload built without the picker) — there is nothing to replace with.
    expect(connectionBindingsChanged(undefined, ["a"])).toBe(false);
    expect(connectionBindingsChanged(undefined, undefined)).toBe(false);
  });

  it("writes when the current bindings are not known yet", () => {
    // An unloaded baseline must never be read as "nothing changed" — that
    // would silently drop a real edit. Erring towards the write is the safe
    // direction: worst case it is the no-op we used to always do.
    expect(connectionBindingsChanged(["a"], undefined)).toBe(true);
    expect(connectionBindingsChanged([], undefined)).toBe(true);
  });
});
