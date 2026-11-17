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

/** Publication templates stand in their group title when no name is known. */
const UNNAMED_KEY = "publication.groupOpenedTitle";

/**
 * Renders a generated sentence in the visitor's language, or `fallback` (the
 * stored literal) when there is no recipe or this build does not know its key.
 * Only `publication.*` keys are rendered: they are the only ones the server
 * puts on a public page.
 */
export function localizeGenerated(
  t: TFunction,
  exists: (key: string) => boolean,
  text: LocalizedText | null | undefined,
  fallback: string,
): string {
  if (!text?.key?.startsWith("publication.") || !exists(text.key)) {
    return fallback;
  }

  const params: Record<string, unknown> = { ...(text.params ?? {}) };
  if (params.name === undefined) params.name = t(UNNAMED_KEY);

  return t(text.key, params) as string;
}
