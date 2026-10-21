import { describe, expect, it } from "vitest";

import { resolveLang, supportedLanguage } from "@/lib/languages";
import {
  planPreferencesSync,
  preferencesSyncMode,
  type PreferencesSyncInput,
} from "@/lib/preferences-sync";

const base: PreferencesSyncInput = {
  server: { locale: "en", timezone: "Europe/Paris" },
  uiLanguage: "en",
  browserTimezone: "Europe/Paris",
  mode: "full",
};

describe("planPreferencesSync", () => {
  it("does nothing when the server already matches the browser", () => {
    expect(planPreferencesSync(base)).toEqual({});
  });

  it("switches the dashboard to the saved language, without writing it", () => {
    expect(
      planPreferencesSync({ ...base, server: { locale: "fr", timezone: "Europe/Paris" } }),
    ).toEqual({ changeLanguage: "fr" });
  });

  it("saves the dashboard language when none is saved", () => {
    expect(
      planPreferencesSync({ ...base, server: { timezone: "Europe/Paris" }, uiLanguage: "de" }),
    ).toEqual({ patches: [{ locale: "de" }] });
  });

  it("saves the primary subtag of a regional UI language", () => {
    expect(
      planPreferencesSync({ ...base, server: { timezone: "Europe/Paris" }, uiLanguage: "es-MX" }),
    ).toEqual({ patches: [{ locale: "es" }] });
  });

  it("never saves a language the server does not support", () => {
    expect(
      planPreferencesSync({ ...base, server: { timezone: "Europe/Paris" }, uiLanguage: "pt" }),
    ).toEqual({});
    expect(
      planPreferencesSync({ ...base, server: { timezone: "Europe/Paris" }, uiLanguage: undefined }),
    ).toEqual({});
  });

  it("saves the browser timezone whenever it differs, even with a saved one", () => {
    expect(planPreferencesSync({ ...base, browserTimezone: "America/New_York" })).toEqual({
      patches: [{ timezone: "America/New_York" }],
    });
  });

  it("saves a first sync's language and timezone apart, the language first", () => {
    // A timezone the server refuses must not take the language down with it.
    expect(planPreferencesSync({ ...base, server: {}, browserTimezone: "Etc/Unknown" })).toEqual({
      patches: [{ locale: "en" }, { timezone: "Etc/Unknown" }],
    });
  });

  it("adopts the saved language and saves the timezone in the same pass", () => {
    expect(
      planPreferencesSync({ ...base, server: { locale: "fr" }, browserTimezone: "Asia/Tokyo" }),
    ).toEqual({ changeLanguage: "fr", patches: [{ timezone: "Asia/Tokyo" }] });
  });

  it("leaves the timezone alone when the browser reports none", () => {
    expect(planPreferencesSync({ ...base, browserTimezone: undefined })).toEqual({});
  });

  it("does nothing against a server that reports no preferences", () => {
    expect(
      planPreferencesSync({ ...base, server: undefined, uiLanguage: "fr", browserTimezone: "UTC" }),
    ).toEqual({});
  });

  it("only adopts, never writes, in adoptOnly mode", () => {
    expect(
      planPreferencesSync({
        ...base,
        server: { locale: "de" },
        browserTimezone: "Asia/Tokyo",
        mode: "adoptOnly",
      }),
    ).toEqual({ changeLanguage: "de" });
    expect(planPreferencesSync({ ...base, server: {}, mode: "adoptOnly" })).toEqual({});
  });

  it("neither adopts nor writes when off", () => {
    expect(
      planPreferencesSync({
        ...base,
        server: { locale: "de" },
        browserTimezone: "Asia/Tokyo",
        mode: "off",
      }),
    ).toEqual({});
    expect(planPreferencesSync({ ...base, server: {}, mode: "off" })).toEqual({});
  });
});

describe("preferencesSyncMode", () => {
  const normal = { isDemo: false, impersonating: false, mustChangePassword: false };

  it("syncs a normal session fully", () => {
    expect(preferencesSyncMode(normal)).toBe("full");
  });

  it("never writes for a session confined to the password rotation", () => {
    expect(preferencesSyncMode({ ...normal, mustChangePassword: true })).toBe("adoptOnly");
  });

  it("stays out of demo and impersonation sessions", () => {
    expect(preferencesSyncMode({ ...normal, isDemo: true })).toBe("off");
    expect(preferencesSyncMode({ ...normal, impersonating: true })).toBe("off");
    expect(
      preferencesSyncMode({ isDemo: false, impersonating: true, mustChangePassword: true }),
    ).toBe("off");
  });
});

describe("supportedLanguage", () => {
  it("maps tags to the four shipped languages", () => {
    expect(supportedLanguage("fr-FR")).toBe("fr");
    expect(supportedLanguage("DE")).toBe("de");
    expect(supportedLanguage("es")).toBe("es");
    expect(supportedLanguage("en-GB")).toBe("en");
    expect(supportedLanguage("pt-BR")).toBeNull();
    expect(supportedLanguage("")).toBeNull();
    expect(supportedLanguage(undefined)).toBeNull();
    expect(resolveLang("pt-BR")).toBe("en");
    expect(resolveLang("fr-CA")).toBe("fr");
  });
});
