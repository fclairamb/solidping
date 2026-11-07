import type { TFunction } from "i18next";

/**
 * Translate function accepted by helpers that receive `t` from any
 * `useTranslation(ns)` call. i18next v27 types `t` per namespace, so a plain
 * `(key, options) => string` no longer accepts it; this wide TFunction does.
 */
export type TranslateFn = TFunction<string | readonly string[]>;

/**
 * Adapts a plain stand-in translator (static catalogs, previews) to
 * TranslateFn. TFunction is branded, so a plain function cannot satisfy it
 * structurally; the stand-in only ever needs the (key, options) call shape.
 */
export function asTranslateFn(
  fn: (key: string, options?: Record<string, unknown>) => string,
): TranslateFn {
  return fn as unknown as TranslateFn;
}
