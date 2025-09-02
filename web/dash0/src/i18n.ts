import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import LanguageDetector from "i18next-browser-languagedetector";
import enCommon from "./locales/en/common.json";
import enNav from "./locales/en/nav.json";
import enAuth from "./locales/en/auth.json";
import enChecks from "./locales/en/checks.json";
import enIncidents from "./locales/en/incidents.json";
import enEvents from "./locales/en/events.json";
import enAccount from "./locales/en/account.json";
import enOrg from "./locales/en/org.json";
import enServer from "./locales/en/server.json";
import enStatusPages from "./locales/en/statusPages.json";
import enBadges from "./locales/en/badges.json";
import frCommon from "./locales/fr/common.json";
import frNav from "./locales/fr/nav.json";
import frAuth from "./locales/fr/auth.json";
import frChecks from "./locales/fr/checks.json";
import frIncidents from "./locales/fr/incidents.json";
import frEvents from "./locales/fr/events.json";
import frAccount from "./locales/fr/account.json";
import frOrg from "./locales/fr/org.json";
import frServer from "./locales/fr/server.json";
import frStatusPages from "./locales/fr/statusPages.json";
import frBadges from "./locales/fr/badges.json";

i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: {
      en: {
        common: enCommon,
        nav: enNav,
        auth: enAuth,
        checks: enChecks,
        incidents: enIncidents,
        events: enEvents,
        account: enAccount,
        org: enOrg,
        server: enServer,
        statusPages: enStatusPages,
        badges: enBadges,
      },
      fr: {
        common: frCommon,
        nav: frNav,
        auth: frAuth,
        checks: frChecks,
        incidents: frIncidents,
        events: frEvents,
        account: frAccount,
        org: frOrg,
        server: frServer,
        statusPages: frStatusPages,
        badges: frBadges,
      },
    },
    defaultNS: "common",
    fallbackLng: "en",
    interpolation: { escapeValue: false },
    detection: {
      order: ["localStorage", "navigator"],
      lookupLocalStorage: "solidping_language",
      caches: ["localStorage"],
    },
  });

export default i18n;
