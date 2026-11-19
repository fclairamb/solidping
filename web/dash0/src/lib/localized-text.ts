import i18n from "@/i18n";
import type { TranslateFn } from "@/lib/i18n-types";

/**
 * The recipe of a sentence the server generated (an incident title, an
 * auto-published status-page entry): a message key plus the values it
 * interpolates. The server stores it next to the literal English (or
 * page-language) text, so the dashboard can show the sentence in the reader's
 * language. Absent when a person wrote or edited the text.
 */
export interface LocalizedText {
  key: string;
  params?: Record<string, string | number>;
}

/** Where the generated-text catalog lives: incidents.json, under `generated`. */
const NAMESPACE = "incidents";
const PREFIX = "generated.";

/** Hand-written text in one language: `body` only on a status update. */
export interface TranslationEntry {
  title: string;
  body?: string;
}

/** Hand-written text in other languages, keyed by language code. */
export type Translations = Record<string, TranslationEntry>;

/**
 * Where a hand-written translation is looked up: the entry's `translations`
 * map and the field it is for. The reader's language is the UI language unless
 * `language` says otherwise (any BCP 47 tag, only its primary subtag counts).
 */
export interface TranslationSource {
  translations?: Translations | null;
  field: "title" | "body";
  language?: string;
}

/** The hand-written text for the reader's language, or undefined. */
export function pickTranslation(source: TranslationSource | undefined): string | undefined {
  if (!source?.translations) return undefined;
  const language = source.language ?? i18n.resolvedLanguage ?? i18n.language;
  if (!language) return undefined;
  const code = language.toLowerCase().split(/[-_]/)[0];
  const value = source.translations[code]?.[source.field];
  return value ? value : undefined;
}

/** Publication templates stand in their group title when no name is known. */
const PUBLICATION_PREFIX = "publication.";
const UNNAMED_PUBLICATION = "publication.groupOpenedTitle";

/**
 * Resolves the text to show a reader, in this order:
 *  1. a hand-written translation for their language (`translation`),
 *  2. the recipe rendered in their language, when the row still has one and
 *     this build knows its key,
 *  3. `fallback`, the stored literal (the page's own language).
 */
export function localizeGenerated(
  t: TranslateFn,
  text: LocalizedText | null | undefined,
  fallback: string,
  translation?: TranslationSource,
): string {
  const written = pickTranslation(translation);
  if (written !== undefined) return written;

  if (!text?.key) return fallback;
  const fullKey = PREFIX + text.key;
  if (!i18n.exists(fullKey, { ns: NAMESPACE })) return fallback;

  const params: Record<string, unknown> = { ...(text.params ?? {}) };
  if (text.key.startsWith(PUBLICATION_PREFIX) && params.name === undefined) {
    params.name = t(PREFIX + UNNAMED_PUBLICATION, { ns: NAMESPACE });
  }

  return t(fullKey, { ns: NAMESPACE, ...params }) as string;
}

/** The UI languages a translation can be written in (same set as the server). */
export const TRANSLATION_LANGUAGES = ["en", "fr", "de", "es"] as const;

/**
 * The payload for a translations draft: groups left empty mean "no
 * translation" and are dropped, a half-filled one is kept so the server can
 * say what is missing. `{}` clears the stored map on a PATCH.
 */
export function cleanTranslations(draft: Translations): Translations {
  const out: Translations = {};
  for (const [lang, entry] of Object.entries(draft)) {
    const title = entry.title ?? "";
    const body = entry.body ?? "";
    if (title.trim() === "" && body.trim() === "") continue;
    out[lang] = body === "" ? { title } : { title, body };
  }
  return out;
}
