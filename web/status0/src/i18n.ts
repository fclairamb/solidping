import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import LanguageDetector from "i18next-browser-languagedetector";
import enStatus from "./locales/en/status.json";
import frStatus from "./locales/fr/status.json";
import deStatus from "./locales/de/status.json";
import esStatus from "./locales/es/status.json";
import zhStatus from "./locales/zh/status.json";

i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: {
      en: { status: enStatus },
      fr: { status: frStatus },
      de: { status: deStatus },
      es: { status: esStatus },
      zh: { status: zhStatus },
    },
    defaultNS: "status",
    fallbackLng: "en",
    // `?lang=` is free text: only the supported languages are accepted, and a
    // regional variant (fr-CA) resolves to its language (fr). Anything else
    // falls back to English instead of reaching Intl as an invalid tag.
    supportedLngs: ["en", "fr", "de", "es", "zh"],
    nonExplicitSupportedLngs: true,
    interpolation: { escapeValue: false },
    detection: {
      order: ["querystring", "navigator"],
      lookupQuerystring: "lang",
    },
  });

export default i18n;
