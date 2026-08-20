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
import enStatusUpdates from "./locales/en/statusUpdates.json";
import enBadges from "./locales/en/badges.json";
import enDashboard from "./locales/en/dashboard.json";
import enFeedback from "./locales/en/feedback.json";
import enDependencies from "./locales/en/dependencies.json";
import enOncall from "./locales/en/oncall.json";
import enEscalation from "./locales/en/escalation.json";
import enIntegrations from "./locales/en/integrations.json";
import enDiscovery from "./locales/en/discovery.json";
import enJobs from "./locales/en/jobs.json";
import enMaintenanceWindows from "./locales/en/maintenanceWindows.json";
import enSlos from "./locales/en/slos.json";
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
import frStatusUpdates from "./locales/fr/statusUpdates.json";
import frBadges from "./locales/fr/badges.json";
import frDashboard from "./locales/fr/dashboard.json";
import frFeedback from "./locales/fr/feedback.json";
import frDependencies from "./locales/fr/dependencies.json";
import frOncall from "./locales/fr/oncall.json";
import frEscalation from "./locales/fr/escalation.json";
import frIntegrations from "./locales/fr/integrations.json";
import frDiscovery from "./locales/fr/discovery.json";
import frJobs from "./locales/fr/jobs.json";
import frMaintenanceWindows from "./locales/fr/maintenanceWindows.json";
import frSlos from "./locales/fr/slos.json";
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
import deStatusUpdates from "./locales/de/statusUpdates.json";
import deBadges from "./locales/de/badges.json";
import deDashboard from "./locales/de/dashboard.json";
import deFeedback from "./locales/de/feedback.json";
import deDependencies from "./locales/de/dependencies.json";
import deOncall from "./locales/de/oncall.json";
import deEscalation from "./locales/de/escalation.json";
import deIntegrations from "./locales/de/integrations.json";
import deDiscovery from "./locales/de/discovery.json";
import deJobs from "./locales/de/jobs.json";
import deMaintenanceWindows from "./locales/de/maintenanceWindows.json";
import deSlos from "./locales/de/slos.json";
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
import esStatusUpdates from "./locales/es/statusUpdates.json";
import esBadges from "./locales/es/badges.json";
import esDashboard from "./locales/es/dashboard.json";
import esFeedback from "./locales/es/feedback.json";
import esDependencies from "./locales/es/dependencies.json";
import esOncall from "./locales/es/oncall.json";
import esEscalation from "./locales/es/escalation.json";
import esIntegrations from "./locales/es/integrations.json";
import esDiscovery from "./locales/es/discovery.json";
import esJobs from "./locales/es/jobs.json";
import esMaintenanceWindows from "./locales/es/maintenanceWindows.json";
import esSlos from "./locales/es/slos.json";

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
        statusUpdates: enStatusUpdates,
        badges: enBadges,
        dashboard: enDashboard,
        feedback: enFeedback,
        dependencies: enDependencies,
        oncall: enOncall,
        escalation: enEscalation,
        integrations: enIntegrations,
        discovery: enDiscovery,
        jobs: enJobs,
        maintenanceWindows: enMaintenanceWindows,
        slos: enSlos,
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
        statusUpdates: frStatusUpdates,
        badges: frBadges,
        dashboard: frDashboard,
        feedback: frFeedback,
        dependencies: frDependencies,
        oncall: frOncall,
        escalation: frEscalation,
        integrations: frIntegrations,
        discovery: frDiscovery,
        jobs: frJobs,
        maintenanceWindows: frMaintenanceWindows,
        slos: frSlos,
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
        statusUpdates: deStatusUpdates,
        badges: deBadges,
        dashboard: deDashboard,
        feedback: deFeedback,
        dependencies: deDependencies,
        oncall: deOncall,
        escalation: deEscalation,
        integrations: deIntegrations,
        discovery: deDiscovery,
        jobs: deJobs,
        maintenanceWindows: deMaintenanceWindows,
        slos: deSlos,
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
        statusUpdates: esStatusUpdates,
        badges: esBadges,
        dashboard: esDashboard,
        feedback: esFeedback,
        dependencies: esDependencies,
        oncall: esOncall,
        escalation: esEscalation,
        integrations: esIntegrations,
        discovery: esDiscovery,
        jobs: esJobs,
        maintenanceWindows: esMaintenanceWindows,
        slos: esSlos,
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
