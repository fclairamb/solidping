/**
 * The language tag handed to `Intl` and `toLocale*` calls.
 *
 * `?lang=` is free text from the URL, so `i18n.language` can hold a value that
 * is not a language tag at all (`123`, `en_`, `a-b-c`). `Intl` throws a
 * RangeError on those and, with no error boundary, the whole page would go
 * blank. This takes the resolved language (already matched against the
 * supported list in i18n.ts), checks that it is a well-formed tag, keeps only
 * its primary subtag and falls back to English.
 */
export interface LanguageState {
  resolvedLanguage?: string;
  language?: string;
}

export function displayLocale(i18n: LanguageState): string {
  for (const candidate of [i18n.resolvedLanguage, i18n.language]) {
    if (!candidate) continue;
    try {
      const [tag] = Intl.getCanonicalLocales(candidate);
      if (tag) return tag.split("-")[0];
    } catch {
      // Not a language tag: try the next candidate, then the fallback.
    }
  }
  return "en";
}
