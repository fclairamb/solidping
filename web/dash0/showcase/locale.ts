/**
 * The language a showcase take is filmed in.
 *
 * `SHOWCASE_LOCALE` picks it (default `en`). The dashboard has no URL or
 * cookie switch: it detects its language from `localStorage`
 * (`solidping_language`), then `navigator.language` (`src/i18n.ts`), and a
 * user's saved `preferences.locale` wins over both once signed in. A fresh
 * database saves nothing until the first full session, which then adopts the
 * browser's language, so setting the browser context's `locale` is enough —
 * and it also drives `Intl`, so dates and numbers on camera match the UI.
 *
 * The staged demo data is localized too: a French screenshot listing
 * "Marketing site" reads as a translation nobody finished. The org name
 * (a company) and the URLs stay as they are.
 *
 * Pure on purpose, so `locale.test.ts` covers it under `bun run test:unit`.
 */

export const SHOWCASE_LOCALES = ["en", "fr", "de", "es"] as const;
export type ShowcaseLocale = (typeof SHOWCASE_LOCALES)[number];

/** Parses `SHOWCASE_LOCALE`. Unset or empty is English; anything unknown throws. */
export function parseShowcaseLocale(raw: string | undefined): ShowcaseLocale {
  const value = (raw ?? "").trim().toLowerCase();
  if (value === "") return "en";
  const match = SHOWCASE_LOCALES.find((l) => l === value);
  if (!match) {
    throw new Error(
      `SHOWCASE_LOCALE=${raw} is not one of ${SHOWCASE_LOCALES.join(", ")}.`,
    );
  }
  return match;
}

/** The BCP 47 tag the browser context reports (`navigator.language`, `Intl`). */
export function browserLocaleTag(locale: ShowcaseLocale): string {
  return { en: "en-US", fr: "fr-FR", de: "de-DE", es: "es-ES" }[locale];
}

/**
 * Where a locale's stills go, relative to the stills directory. English keeps
 * the flat layout every published path already depends on.
 */
export function localeSubdir(locale: ShowcaseLocale): string {
  return locale === "en" ? "" : locale;
}

export interface DemoCheck {
  name: string;
  type: "http";
  config: { url: string };
}

export interface ShowcaseContent {
  /** Checks staged before filming, so the list looks like a real account. */
  demoChecks: DemoCheck[];
  /** The check created on camera. */
  featured: { name: string; url: string };
  /** Display name in the sidebar footer. */
  userName: string;
}

const URLS = {
  marketing: "https://www.solidping.io/",
  docs: "https://docs.solidping.io/",
  checkout: "https://api.solidping.io/health",
  featured: "https://solidping.io/api/mgmt/health",
};

const NAMES: Record<
  ShowcaseLocale,
  { marketing: string; docs: string; checkout: string; featured: string; user: string }
> = {
  en: {
    marketing: "Marketing site",
    docs: "Docs site",
    checkout: "Checkout API",
    featured: "Production API",
    user: "Alex Rivera",
  },
  fr: {
    marketing: "Site vitrine",
    docs: "Documentation",
    checkout: "API de paiement",
    featured: "API de production",
    user: "Camille Martin",
  },
  de: {
    marketing: "Website",
    docs: "Dokumentation",
    checkout: "Checkout-API",
    featured: "Produktions-API",
    user: "Lena Becker",
  },
  es: {
    marketing: "Sitio web",
    docs: "Documentación",
    checkout: "API de pagos",
    featured: "API de producción",
    user: "Lucía García",
  },
};

/** The demo content for a locale. */
export function showcaseContent(locale: ShowcaseLocale): ShowcaseContent {
  const n = NAMES[locale];
  return {
    demoChecks: [
      { name: n.marketing, type: "http", config: { url: URLS.marketing } },
      { name: n.docs, type: "http", config: { url: URLS.docs } },
      { name: n.checkout, type: "http", config: { url: URLS.checkout } },
    ],
    featured: { name: n.featured, url: URLS.featured },
    userName: n.user,
  };
}

/** The words burned into the published cut. */
export interface ShowcaseCaptions {
  /** Over the `docker run` terminal segment. */
  run: string;
  /** Over the first sign-in and the forced password rotation. */
  login: string;
  /** Over creating the check. */
  check: string;
  /** Over the detail page as results arrive. */
  results: string;
  /** The tag over the sped-up stretch, for a given speed. */
  speed: (factor: number) => string;
}

const CAPTIONS: Record<ShowcaseLocale, ShowcaseCaptions> = {
  en: {
    run: "Run it",
    login: "First login",
    check: "First check",
    results: "First results",
    speed: (n) => `${n}× speed`,
  },
  fr: {
    run: "Lancez-le",
    login: "Première connexion",
    check: "Premier contrôle",
    results: "Premiers résultats",
    speed: (n) => `Accéléré ×${n}`,
  },
  de: {
    run: "Starten",
    login: "Erste Anmeldung",
    check: "Erster Check",
    results: "Erste Ergebnisse",
    speed: (n) => `${n}× schneller`,
  },
  es: {
    run: "Ejecútalo",
    login: "Primer inicio de sesión",
    check: "Primera comprobación",
    results: "Primeros resultados",
    speed: (n) => `Velocidad ×${n}`,
  },
};

/** The captions of the published cut, in the locale's language. */
export function showcaseCaptions(locale: ShowcaseLocale): ShowcaseCaptions {
  return CAPTIONS[locale];
}
