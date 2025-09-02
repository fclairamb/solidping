import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import LanguageDetector from "i18next-browser-languagedetector";
import enStatus from "./locales/en/status.json";
import frStatus from "./locales/fr/status.json";

i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: {
      en: { status: enStatus },
      fr: { status: frStatus },
    },
    defaultNS: "status",
    fallbackLng: "en",
    interpolation: { escapeValue: false },
    detection: {
      order: ["querystring", "navigator"],
      lookupQuerystring: "lang",
    },
  });

export default i18n;
