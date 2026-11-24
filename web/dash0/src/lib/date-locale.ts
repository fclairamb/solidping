import { useTranslation } from "react-i18next";
import type { Locale } from "date-fns";
import { de, enUS, es, fr, zhCN } from "date-fns/locale";

const LOCALES: Record<string, Locale> = { en: enUS, fr, de, es, zh: zhCN };

/** Maps an i18n language code (e.g. "fr", "fr-CA") to a date-fns locale. */
export function dateFnsLocale(language?: string): Locale {
  const base = (language ?? "").toLowerCase().split("-")[0];
  return LOCALES[base] ?? enUS;
}

/** date-fns locale matching the current dashboard language. */
export function useDateFnsLocale(): Locale {
  const { i18n } = useTranslation();
  return dateFnsLocale(i18n.resolvedLanguage ?? i18n.language);
}
