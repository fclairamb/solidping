import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import LanguageDetector from "i18next-browser-languagedetector";
import en_account from "./locales/en/account.json";
import en_auth from "./locales/en/auth.json";
import en_badges from "./locales/en/badges.json";
import en_checks from "./locales/en/checks.json";
import en_common from "./locales/en/common.json";
import en_dashboard from "./locales/en/dashboard.json";
import en_dependencies from "./locales/en/dependencies.json";
import en_discovery from "./locales/en/discovery.json";
import en_escalation from "./locales/en/escalation.json";
import en_events from "./locales/en/events.json";
import en_feedback from "./locales/en/feedback.json";
import en_incidents from "./locales/en/incidents.json";
import en_integrations from "./locales/en/integrations.json";
import en_jobs from "./locales/en/jobs.json";
import en_maintenanceWindows from "./locales/en/maintenanceWindows.json";
import en_nav from "./locales/en/nav.json";
import en_oncall from "./locales/en/oncall.json";
import en_org from "./locales/en/org.json";
import en_server from "./locales/en/server.json";
import en_slos from "./locales/en/slos.json";
import en_statusPages from "./locales/en/statusPages.json";
import en_statusUpdates from "./locales/en/statusUpdates.json";
import en_support from "./locales/en/support.json";

// English is the fallback language and stays in the main chunk. fr, de and es
// each live in one lazily loaded chunk (all namespaces together), so a visitor
// only downloads and parses the language they use.
type Bundle = Record<string, Record<string, unknown>>;

export const localeLoaders: Record<string, () => Promise<{ default: Bundle }>> = {
  fr: () => import("./locales/bundles/fr"),
  de: () => import("./locales/bundles/de"),
  es: () => import("./locales/bundles/es"),
};

const loaded = new Map<string, Promise<void>>();

function baseLanguage(lng: string): string {
  return lng.split("-")[0].toLowerCase();
}

/** Loads a language's translations (once). Rejects if the chunk fails; a failed attempt is retried next time. */
export function loadLanguage(lng: string): Promise<void> {
  const base = baseLanguage(lng);
  const loader = localeLoaders[base];
  if (!loader) return Promise.resolve();
  let p = loaded.get(base);
  if (!p) {
    p = loader().then((mod) => {
      for (const [ns, data] of Object.entries(mod.default)) {
        i18n.addResourceBundle(base, ns, data, true, true);
      }
    });
    p = p.catch((err) => {
      loaded.delete(base);
      throw err;
    });
    loaded.set(base, p);
  }
  return p;
}

const enResources = {
  account: en_account,
  auth: en_auth,
  badges: en_badges,
  checks: en_checks,
  common: en_common,
  dashboard: en_dashboard,
  dependencies: en_dependencies,
  discovery: en_discovery,
  escalation: en_escalation,
  events: en_events,
  feedback: en_feedback,
  incidents: en_incidents,
  integrations: en_integrations,
  jobs: en_jobs,
  maintenanceWindows: en_maintenanceWindows,
  nav: en_nav,
  oncall: en_oncall,
  org: en_org,
  server: en_server,
  slos: en_slos,
  statusPages: en_statusPages,
  statusUpdates: en_statusUpdates,
  support: en_support,
};

const initPromise = i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: { en: enResources },
    defaultNS: "common",
    fallbackLng: "en",
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
    detection: {
      order: ["localStorage", "navigator"],
      lookupLocalStorage: "solidping_language",
      caches: ["localStorage"],
    },
  });

// changeLanguage resolves only once the target language's chunk is loaded. If
// the load fails it rejects and the current language stays active.
const originalChangeLanguage = i18n.changeLanguage.bind(i18n);
i18n.changeLanguage = (async (lng?: string, callback?: Parameters<typeof originalChangeLanguage>[1]) => {
  if (lng) await loadLanguage(lng);
  return originalChangeLanguage(lng, callback);
}) as typeof i18n.changeLanguage;

const LOAD_TIMEOUT_MS = 2000;

/**
 * Resolves once the detected language is ready to render. If its chunk fails
 * or takes longer than ~2 s, English is rendered rather than a blank page.
 */
export const i18nReady: Promise<void> = initPromise.then(async () => {
  const detected = i18n.language;
  if (!detected || baseLanguage(detected) === "en") return;
  const load = i18n.changeLanguage(detected).then(() => undefined);
  const timeout = new Promise<void>((resolve) => setTimeout(resolve, LOAD_TIMEOUT_MS));
  try {
    await Promise.race([load, timeout]);
  } catch {
    // Keep English.
  }
});

export default i18n;
