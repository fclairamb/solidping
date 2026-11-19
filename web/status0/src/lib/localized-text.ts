import type { TFunction } from "i18next";

/**
 * The recipe of a sentence the server generated for an auto-published
 * incident (its title, its updates): a message key plus the values it
 * interpolates. The server stores it next to the literal text, written in the
 * page's language, so a visitor reading the page in another language gets the
 * sentence in theirs. Absent when a person wrote or edited the text.
 */
export interface LocalizedText {
  key: string;
  params?: Record<string, string | number>;
}

/** Hand-written text in one language: `body` only on a status update. */
export interface TranslationEntry {
  title: string;
  body?: string;
}

/** Hand-written text in other languages, keyed by language code. */
export type Translations = Record<string, TranslationEntry>;

/**
 * Where a hand-written translation is looked up: the entry's `translations`
 * map, the field it is for, and the reader's language (any BCP 47 tag, only
 * its primary subtag counts).
 */
export interface TranslationSource {
  translations?: Translations | null;
  field: "title" | "body";
  language?: string;
}

/** The hand-written text for the reader's language, or undefined. */
export function pickTranslation(source: TranslationSource | undefined): string | undefined {
  if (!source?.translations || !source.language) return undefined;
  const code = source.language.toLowerCase().split(/[-_]/)[0];
  const value = source.translations[code]?.[source.field];
  return value ? value : undefined;
}

/** Publication templates stand in their group title when no name is known. */
const UNNAMED_KEY = "publication.groupOpenedTitle";

/**
 * Resolves the text to show a reader, in this order:
 *  1. a hand-written translation for their language (`translation`),
 *  2. the recipe rendered in their language, when the row still has one and
 *     this build knows its key,
 *  3. `fallback`, the stored literal (the page's own language).
 * Only `publication.*` recipe keys are rendered: they are the only ones the
 * server puts on a public page.
 */
export function localizeGenerated(
  t: TFunction,
  exists: (key: string) => boolean,
  text: LocalizedText | null | undefined,
  fallback: string,
  translation?: TranslationSource,
): string {
  const written = pickTranslation(translation);
  if (written !== undefined) return written;

  if (!text?.key?.startsWith("publication.") || !exists(text.key)) {
    return fallback;
  }

  const params: Record<string, unknown> = { ...(text.params ?? {}) };
  if (params.name === undefined) params.name = t(UNNAMED_KEY);

  return t(text.key, params) as string;
}
