import { describe, expect, it } from "vitest";
import type { TFunction } from "i18next";

import type { SupportThread } from "@/api/support";
import {
  supportReplyBlockReason,
  supportReplyBlockTitle,
} from "@/lib/support-reply-block";

import supportEn from "@/locales/en/support.json";
import supportFr from "@/locales/fr/support.json";
import supportDe from "@/locales/de/support.json";
import supportEs from "@/locales/es/support.json";

/**
 * A `t` backed by a real locale bundle that returns the KEY on a miss, exactly
 * as i18next would. That is what makes these tests a locale gate: a fallback
 * pointing at a key nobody translated leaks `reply.disabledNoRoute` into the UI
 * instead of a sentence.
 */
function tFor(bundle: unknown): TFunction {
  const resolve = (key: string): string => {
    let node: unknown = bundle;
    for (const part of key.split(".")) {
      if (typeof node !== "object" || node === null || !(part in node)) return key;
      node = (node as Record<string, unknown>)[part];
    }

    return typeof node === "string" ? node : key;
  };

  return resolve as unknown as TFunction;
}

function thread(overrides: Partial<SupportThread> = {}): SupportThread {
  return {
    uid: "t1",
    channel: "slack",
    channelIdentity: "U0ACME1234",
    subject: "U0ACME1234: hi",
    status: "open",
    lastMessageAt: "2026-08-27T10:00:00Z",
    unreadCount: 0,
    createdAt: "2026-08-27T10:00:00Z",
    updatedAt: "2026-08-27T10:00:00Z",
    replyWindow: { expires: false, open: true, costsMoney: false },
    canReply: true,
    ...overrides,
  };
}

const t = tFor(supportEn);

describe("supportReplyBlockReason", () => {
  it("returns nothing for an answerable thread", () => {
    expect(supportReplyBlockReason(thread(), t)).toBe("");
  });

  it("prefers the server's per-thread routing reason", () => {
    // The whole point of the spec: the operator is told the workspace was never
    // connected, not a generic "no adapter" that would be wrong.
    const reason =
      "SolidPing holds no bot token for this Slack workspace — the app must be " +
      "installed through SolidPing before replies can be sent";

    expect(
      supportReplyBlockReason(thread({ canReply: false, canReplyReason: reason }), t),
    ).toBe(reason);
  });

  it("falls back to a translated sentence when the server sent no reason", () => {
    const blocked = supportReplyBlockReason(thread({ canReply: false }), t);

    expect(blocked).toBe("This thread cannot be replied to from here.");
    expect(blocked).not.toContain("disabledNoRoute");
  });

  it("still reports a lapsed WhatsApp window on a routable thread", () => {
    const reason = "the 24-hour WhatsApp customer-service window has lapsed";

    expect(
      supportReplyBlockReason(
        thread({
          channel: "whatsapp",
          replyWindow: { expires: true, open: false, reason, costsMoney: false },
        }),
        t,
      ),
    ).toBe(reason);
  });

  it("reports the routing problem first when both axes are blocked", () => {
    // A thread with no route and a lapsed window is unanswerable for the
    // routing reason regardless of the clock; telling the operator to wait for
    // a window would send them after the wrong fix.
    expect(
      supportReplyBlockReason(
        thread({
          canReply: false,
          canReplyReason: "no bot token",
          replyWindow: { expires: true, open: false, reason: "lapsed", costsMoney: false },
        }),
        t,
      ),
    ).toBe("no bot token");
  });
});

describe("supportReplyBlockTitle", () => {
  it("distinguishes a lapsed window from a missing route", () => {
    expect(supportReplyBlockTitle(thread({ canReply: false }), t)).toBe(
      "Cannot reply to this thread",
    );
    expect(
      supportReplyBlockTitle(
        thread({ replyWindow: { expires: true, open: false, costsMoney: false } }),
        t,
      ),
    ).toBe("Reply window closed");
  });
});

describe("locale coverage", () => {
  // Every key these helpers can reach must exist in every shipped locale — a
  // missing one renders the dotted key to the operator.
  const bundles = { en: supportEn, fr: supportFr, de: supportDe, es: supportEs };

  for (const [lang, bundle] of Object.entries(bundles)) {
    it(`has every reply-block key in ${lang}`, () => {
      const translate = tFor(bundle);

      for (const key of [
        "reply.disabledNoRoute",
        "reply.resend",
        "reply.resending",
        "reply.resent",
        "reply.resendFailed",
        "window.expired",
        "window.cannotReply",
        "thread.deliveryFailed",
      ]) {
        expect(translate(key), `${lang} is missing ${key}`).not.toBe(key);
      }
    });
  }
});
