import { supportedLanguage, type LanguageCode } from "@/lib/languages";

/** `?lang=fr` on any dashboard URL switches the dashboard to French. */
export const LANGUAGE_QUERY_PARAM = "lang";

export interface LanguageQuery {
  /** The supported language asked for, or null (absent or unknown value). */
  language: LanguageCode | null;
  /** The URL without the parameter, or null when it carried none. */
  cleanedUrl: string | null;
}

/**
 * Reads `?lang=` from a URL. The parameter is removed from the URL even when
 * its value is not a language we ship, so a stale `?lang=xx` link does not
 * linger in the address bar.
 */
export function parseLanguageQuery(href: string): LanguageQuery {
  const url = new URL(href);
  if (!url.searchParams.has(LANGUAGE_QUERY_PARAM)) {
    return { language: null, cleanedUrl: null };
  }
  const language = supportedLanguage(url.searchParams.get(LANGUAGE_QUERY_PARAM));
  url.searchParams.delete(LANGUAGE_QUERY_PARAM);
  return { language, cleanedUrl: `${url.pathname}${url.search}${url.hash}` };
}

let requested: LanguageCode | null = null;

/**
 * Takes `?lang=` from the current page URL, once, before i18next and the router
 * start: the language is written where the detector looks first
 * (localStorage), and the parameter is dropped from the address bar so a later
 * pick in the language menu is not undone on reload.
 */
export function consumeLanguageQuery(storageKey: string): LanguageCode | null {
  if (typeof window === "undefined") return null;
  const { language, cleanedUrl } = parseLanguageQuery(window.location.href);
  if (cleanedUrl !== null) {
    window.history.replaceState(window.history.state, "", cleanedUrl);
  }
  if (language) {
    requested = language;
    try {
      window.localStorage.setItem(storageKey, language);
    } catch {
      // Storage disabled: the detector still gets it through the fallback below.
    }
  }
  return language;
}

/**
 * The language this page load was opened with through `?lang=`, if any. It
 * counts as an explicit pick: a signed-in user's saved language is replaced by
 * it rather than overriding it.
 */
export function requestedLanguage(): LanguageCode | null {
  return requested;
}

/** Forgets the `?lang=` pick: a later pick in the language menu replaces it. */
export function clearRequestedLanguage(): void {
  requested = null;
}
