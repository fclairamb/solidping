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

/** Publication templates stand in their group title when no name is known. */
const PUBLICATION_PREFIX = "publication.";
const UNNAMED_PUBLICATION = "publication.groupOpenedTitle";

/**
 * Renders a generated sentence in the reader's language, or `fallback` (the
 * stored literal) when there is no recipe or this build does not know its key.
 */
export function localizeGenerated(
  t: TranslateFn,
  text: LocalizedText | null | undefined,
  fallback: string,
): string {
  if (!text?.key) return fallback;
  const fullKey = PREFIX + text.key;
  if (!i18n.exists(fullKey, { ns: NAMESPACE })) return fallback;

  const params: Record<string, unknown> = { ...(text.params ?? {}) };
  if (text.key.startsWith(PUBLICATION_PREFIX) && params.name === undefined) {
    params.name = t(PREFIX + UNNAMED_PUBLICATION, { ns: NAMESPACE });
  }

  return t(fullKey, { ns: NAMESPACE, ...params }) as string;
}
