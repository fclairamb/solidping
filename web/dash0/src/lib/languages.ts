/**
 * The languages dash0 ships, which are also the languages the server writes
 * emails in (spec 2026-10-21-01). Labels are endonyms: each language is named
 * in itself, so they are never translated.
 */
export const LANGUAGES = [
  { code: "en", flag: "\u{1F1FA}\u{1F1F8}", label: "English" },
  { code: "fr", flag: "\u{1F1EB}\u{1F1F7}", label: "Français" },
  { code: "de", flag: "\u{1F1E9}\u{1F1EA}", label: "Deutsch" },
  { code: "es", flag: "\u{1F1EA}\u{1F1F8}", label: "Español" },
] as const;

export type LanguageCode = (typeof LANGUAGES)[number]["code"];

/**
 * The supported language a tag maps to ("fr-FR" -> "fr"), or null when it is
 * not one of ours. Same rule as the server's i18n.Normalize.
 */
export function supportedLanguage(tag: string | undefined | null): LanguageCode | null {
  const prefix = tag?.split("-")[0]?.trim().toLowerCase();
  return LANGUAGES.find((l) => l.code === prefix)?.code ?? null;
}

/** Like supportedLanguage, with English for anything else. */
export function resolveLang(tag: string | undefined | null): LanguageCode {
  return supportedLanguage(tag) ?? "en";
}

/** The browser's IANA timezone, or undefined when it reports none. */
export function browserTimezone(): string | undefined {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || undefined;
  } catch {
    return undefined;
  }
}
