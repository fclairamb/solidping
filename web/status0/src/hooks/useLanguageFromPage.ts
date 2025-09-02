import { useEffect } from "react";
import { useTranslation } from "react-i18next";

/**
 * Sets the i18n language based on priority:
 * 1. ?lang= query parameter (handled by i18next-browser-languagedetector)
 * 2. Status page's configured language from the API
 * 3. Browser language (handled by i18next-browser-languagedetector)
 */
export function useLanguageFromPage(pageLanguage?: string) {
  const { i18n } = useTranslation();

  useEffect(() => {
    // If ?lang= is in the URL, the detector already handled it — don't override
    const params = new URLSearchParams(window.location.search);
    if (params.has("lang")) return;

    // Use the page's configured language if available
    if (pageLanguage && i18n.language !== pageLanguage) {
      i18n.changeLanguage(pageLanguage);
    }
  }, [pageLanguage, i18n]);
}
