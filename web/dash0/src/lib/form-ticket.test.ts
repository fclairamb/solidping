import { describe, expect, it } from "vitest";
import { FORM_TICKET_MARGIN_MS, formTicketDelayMs } from "./form-ticket";

describe("formTicketDelayMs", () => {
  it("waits out the rest of the minimum age plus the margin", () => {
    expect(formTicketDelayMs(3000, 10_000, 11_000)).toBe(2000 + FORM_TICKET_MARGIN_MS);
  });

  it("does not wait for a ticket that is already old enough", () => {
    expect(formTicketDelayMs(3000, 10_000, 20_000)).toBe(0);
  });

  it("does not wait when the server asks for no minimum age", () => {
    expect(formTicketDelayMs(0, 10_000, 10_000 + FORM_TICKET_MARGIN_MS)).toBe(0);
  });
});
