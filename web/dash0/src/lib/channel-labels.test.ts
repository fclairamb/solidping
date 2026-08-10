import { describe, expect, it } from "vitest";
import type { TFunction } from "i18next";

import { channelTypeLabel, failureReasonLabel } from "@/lib/channel-labels";

import commonEn from "@/locales/en/common.json";
import commonFr from "@/locales/fr/common.json";
import commonDe from "@/locales/de/common.json";
import commonEs from "@/locales/es/common.json";

type Bundle = { channels: Record<string, string>; notificationFailureReasons: Record<string, string> };

/**
 * A `t` backed by a real locale bundle, resolving dotted keys the way i18next
 * does — and, crucially, returning the KEY on a miss, exactly as i18next would.
 * That is what makes the "unknown token" tests meaningful: a helper that
 * blindly called t() would leak `channels.carrier_pigeon` into the UI.
 */
function tFor(bundle: unknown): TFunction {
  const resolve = (key: string): string => {
    const parts = key.split(".");
    let node: unknown = bundle;
    for (const part of parts) {
      if (typeof node !== "object" || node === null || !(part in node)) return key;
      node = (node as Record<string, unknown>)[part];
    }

    return typeof node === "string" ? node : key;
  };

  return resolve as unknown as TFunction;
}

const t = tFor(commonEn);

describe("channelTypeLabel", () => {
  it("renders the correctly-cased brand name for whatsapp", () => {
    // The whole reason this map exists: CSS `capitalize` yields "Whatsapp".
    expect(channelTypeLabel(t, "whatsapp")).toBe("WhatsApp");
  });

  it("renders other known channels from the locale bundle", () => {
    expect(channelTypeLabel(t, "sms")).toBe("SMS");
    expect(channelTypeLabel(t, "webpush")).toBe("Browser push");
    expect(channelTypeLabel(t, "msteams-bot")).toBe("Microsoft Teams");
  });

  it("renders Telegram from the locale bundle rather than raw-capitalising it", () => {
    expect(channelTypeLabel(t, "telegram")).toBe("Telegram");
  });

  it("capitalises an unknown channel rather than leaking an i18n key", () => {
    expect(channelTypeLabel(t, "carrier_pigeon")).toBe("Carrier_pigeon");
    expect(channelTypeLabel(t, "carrier_pigeon")).not.toContain("channels.");
  });

  it("returns an empty string for a missing channel", () => {
    expect(channelTypeLabel(t, undefined)).toBe("");
    expect(channelTypeLabel(t, null)).toBe("");
    expect(channelTypeLabel(t, "")).toBe("");
  });
});

describe("failureReasonLabel", () => {
  it("explains each WhatsApp failure class in prose", () => {
    expect(failureReasonLabel(t, "whatsapp_recipient_not_on_whatsapp")).toBe(
      "Recipient is not on WhatsApp",
    );
    expect(failureReasonLabel(t, "whatsapp_rate_limited")).toContain("Rate limited");
    expect(failureReasonLabel(t, "whatsapp_quota_exhausted")).toContain("quota");
  });

  it("never blames the recipient for a rate-limit or payload error", () => {
    // 131049 (pacing) and 131051 (our payload) must not read like a bad number.
    for (const reason of ["whatsapp_rate_limited", "whatsapp_unsupported_message"]) {
      expect(failureReasonLabel(t, reason)).not.toContain("not on WhatsApp");
    }
  });

  it("explains each Telegram failure class in prose", () => {
    // The two that need an ACTION from the user, not just a description.
    expect(failureReasonLabel(t, "telegram_bot_blocked")).toContain("reconnect");
    expect(failureReasonLabel(t, "telegram_chat_not_found")).toContain("reconnect");
    // …and the ones that are an operator or transient problem must NOT tell the
    // user to reconnect, which would send them chasing the wrong fix.
    expect(failureReasonLabel(t, "telegram_rate_limited")).not.toContain("reconnect");
    expect(failureReasonLabel(t, "telegram_unauthorized")).not.toContain("reconnect");
    expect(failureReasonLabel(t, "telegram_not_configured")).not.toContain("reconnect");
  });

  it("de-snake-cases an unknown reason rather than leaking an i18n key", () => {
    expect(failureReasonLabel(t, "some_new_backend_reason")).toBe("some new backend reason");
    expect(failureReasonLabel(t, "some_new_backend_reason")).not.toContain(
      "notificationFailureReasons.",
    );
  });

  it("returns an empty string for a missing reason", () => {
    expect(failureReasonLabel(t, undefined)).toBe("");
    expect(failureReasonLabel(t, null)).toBe("");
  });
});

describe("locale parity", () => {
  const locales: Record<string, unknown> = {
    fr: commonFr,
    de: commonDe,
    es: commonEs,
  };

  const enBundle = commonEn as unknown as Bundle;

  it.each(Object.keys(locales))(
    "%s translates every channel and failure reason that en does",
    (locale) => {
      const bundle = locales[locale] as unknown as Bundle;

      expect(Object.keys(bundle.channels).sort()).toEqual(
        Object.keys(enBundle.channels).sort(),
      );
      expect(Object.keys(bundle.notificationFailureReasons).sort()).toEqual(
        Object.keys(enBundle.notificationFailureReasons).sort(),
      );
    },
  );

  it.each(Object.keys(locales))("%s resolves labels through the helpers", (locale) => {
    const localeT = tFor(locales[locale]);

    // Every token the helpers claim to translate must resolve in every locale —
    // a missing key would surface the raw dotted key to the user.
    for (const channel of Object.keys(enBundle.channels)) {
      const label = channelTypeLabel(localeT, channel);
      expect(label).not.toContain("channels.");
      expect(label.length).toBeGreaterThan(0);
    }

    for (const reason of Object.keys(enBundle.notificationFailureReasons)) {
      const label = failureReasonLabel(localeT, reason);
      expect(label).not.toContain("notificationFailureReasons.");
      expect(label.length).toBeGreaterThan(0);
    }
  });

  it("actually differs from English where a translation is expected", () => {
    // Positive control for the parity test above: identical key sets would also
    // be satisfied by copying the English file verbatim.
    expect(failureReasonLabel(tFor(commonFr), "whatsapp_recipient_not_on_whatsapp")).not.toBe(
      failureReasonLabel(t, "whatsapp_recipient_not_on_whatsapp"),
    );
    expect(channelTypeLabel(tFor(commonDe), "voice")).not.toBe(channelTypeLabel(t, "voice"));
  });
});
