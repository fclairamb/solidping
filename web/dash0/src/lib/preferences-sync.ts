import { supportedLanguage } from "@/lib/languages";

/**
 * The language and timezone the server writes a user's emails in
 * (`user.preferences` on /auth/me and every login response, spec
 * 2026-10-21-01). An empty field means "never set".
 */
export interface UserPreferences {
  locale?: string;
  timezone?: string;
}

/** The body of `PATCH /auth/me {preferences}`: each field optional. */
export type PreferencesPatch = UserPreferences;

/**
 * How far a session may take part in the sync:
 * - `full`: adopt the saved language and write the browser's values;
 * - `adoptOnly`: adopt the saved language, never write (the server answers
 *   403 to every write of a session confined to the password rotation screen);
 * - `off`: neither. An impersonating admin or a demo visitor is looking at
 *   someone else's account: adopting its language would rewrite this
 *   browser's own (i18next caches it), and their writes are refused anyway.
 */
export type PreferencesSyncMode = "full" | "adoptOnly" | "off";

export function preferencesSyncMode(session: {
  isDemo: boolean;
  impersonating: boolean;
  mustChangePassword: boolean;
}): PreferencesSyncMode {
  if (session.isDemo || session.impersonating) {
    return "off";
  }
  if (session.mustChangePassword) {
    return "adoptOnly";
  }
  return "full";
}

export interface PreferencesSyncInput {
  /**
   * The server's copy. undefined when the response carried none (a server
   * that predates preferences): nothing to sync against.
   */
  server: UserPreferences | undefined;
  /** The dashboard's current language (`i18n.resolvedLanguage`). */
  uiLanguage: string | undefined;
  /** The browser's IANA timezone. */
  browserTimezone: string | undefined;
  mode: PreferencesSyncMode;
}

export interface PreferencesSyncPlan {
  /** Switch the dashboard to the user's saved language. */
  changeLanguage?: string;
  /**
   * Writes to send, in order, one field each. The server validates a PATCH
   * as a whole, so a timezone it does not know (ICU's "Etc/Unknown", a zone
   * missing from its tzdata) sent along with the language would lose the
   * language too. Apart, the language goes first and is saved either way.
   */
  patches?: PreferencesPatch[];
}

/**
 * Decides what AuthProvider does when a session's preferences arrive (spec
 * 2026-10-21-01, decision 5):
 * - a saved language wins: the dashboard switches to it;
 * - no saved language: the dashboard's current one is saved;
 * - the timezone always follows the browser, saved whenever it differs.
 */
export function planPreferencesSync(input: PreferencesSyncInput): PreferencesSyncPlan {
  const plan: PreferencesSyncPlan = {};
  if (!input.server || input.mode === "off") {
    return plan;
  }

  const patches: PreferencesPatch[] = [];
  const saved = supportedLanguage(input.server.locale);
  const current = supportedLanguage(input.uiLanguage);
  if (saved) {
    if (saved !== current) {
      plan.changeLanguage = saved;
    }
  } else if (input.mode === "full" && current) {
    patches.push({ locale: current });
  }

  if (
    input.mode === "full" &&
    input.browserTimezone &&
    input.browserTimezone !== input.server.timezone
  ) {
    patches.push({ timezone: input.browserTimezone });
  }

  if (patches.length > 0) {
    plan.patches = patches;
  }

  return plan;
}
