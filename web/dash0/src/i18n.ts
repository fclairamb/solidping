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
import enDashboard from "./locales/en/dashboard.json";
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
import frDashboard from "./locales/fr/dashboard.json";
import deCommon from "./locales/de/common.json";
import deNav from "./locales/de/nav.json";
import deAuth from "./locales/de/auth.json";
import deChecks from "./locales/de/checks.json";
import deIncidents from "./locales/de/incidents.json";
import deEvents from "./locales/de/events.json";
import deAccount from "./locales/de/account.json";
import deOrg from "./locales/de/org.json";
import deServer from "./locales/de/server.json";
import deStatusPages from "./locales/de/statusPages.json";
import deBadges from "./locales/de/badges.json";
import deDashboard from "./locales/de/dashboard.json";
import esCommon from "./locales/es/common.json";
import esNav from "./locales/es/nav.json";
import esAuth from "./locales/es/auth.json";
import esChecks from "./locales/es/checks.json";
import esIncidents from "./locales/es/incidents.json";
import esEvents from "./locales/es/events.json";
import esAccount from "./locales/es/account.json";
import esOrg from "./locales/es/org.json";
import esServer from "./locales/es/server.json";
import esStatusPages from "./locales/es/statusPages.json";
import esBadges from "./locales/es/badges.json";
import esDashboard from "./locales/es/dashboard.json";

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
        dashboard: enDashboard,
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
        dashboard: frDashboard,
      },
      de: {
        common: deCommon,
        nav: deNav,
        auth: deAuth,
        checks: deChecks,
        incidents: deIncidents,
        events: deEvents,
        account: deAccount,
        org: deOrg,
        server: deServer,
        statusPages: deStatusPages,
        badges: deBadges,
        dashboard: deDashboard,
      },
      es: {
        common: esCommon,
        nav: esNav,
        auth: esAuth,
        checks: esChecks,
        incidents: esIncidents,
        events: esEvents,
        account: esAccount,
        org: esOrg,
        server: esServer,
        statusPages: esStatusPages,
        badges: esBadges,
        dashboard: esDashboard,
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
