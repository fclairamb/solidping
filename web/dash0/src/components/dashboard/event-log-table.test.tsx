/**
 * @vitest-environment jsdom
 *
 * solidping-business#59: a failed sign-in row says which account was tried,
 * how many attempts it folds, and (for admins) the member it belongs to.
 */
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import i18next from "i18next";
import type { ReactNode } from "react";

import eventsEn from "@/locales/en/events.json";
import type { TranslateFn } from "@/lib/i18n-types";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => <a>{children}</a>,
}));

import { EventLogTable } from "@/components/dashboard/event-log-table";

let t: TranslateFn;

beforeAll(async () => {
  const instance = i18next.createInstance();
  await instance.init({
    lng: "en",
    resources: { en: { events: eventsEn } },
    interpolation: { escapeValue: false },
  });
  t = instance.getFixedT("en", "events") as unknown as TranslateFn;
});

afterEach(cleanup);

const failedLogin = (extra: Record<string, unknown>, targetUserName?: string) => ({
  uid: "evt-1",
  eventType: "auth.login_failed",
  actorType: "system" as const,
  createdAt: new Date().toISOString(),
  targetUserName,
  payload: { reason: "invalid_credentials", target_type: "user", ...extra },
});

describe("EventLogTable failed sign-in detail", () => {
  it("renders the tried account and the folded count", () => {
    render(
      <EventLogTable
        org="acme"
        t={t}
        events={[failedLogin({ email: "bob@acme.com", target_name: "bob@acme.com", count: 4 })]}
      />,
    );
    expect(screen.getByTestId("event-detail-auth-account").textContent).toBe("bob@acme.com × 4");
  });

  it("adds the member name when the API resolved one, and no count for a single attempt", () => {
    render(
      <EventLogTable
        org="acme"
        t={t}
        events={[failedLogin({ email: "bob@acme.com", count: 1 }, "Bob Person")]}
      />,
    );
    expect(screen.getByTestId("event-detail-auth-account").textContent).toBe(
      "bob@acme.com (Bob Person)",
    );
  });

  it("falls back to target_name when the payload has no email", () => {
    render(
      <EventLogTable org="acme" t={t} events={[failedLogin({ target_name: "ghost@acme.com" })]} />,
    );
    expect(screen.getByTestId("event-detail-auth-account").textContent).toBe("ghost@acme.com");
  });
});
