import { describe, test, expect } from "bun:test";
import type { TFunction } from "i18next";
import en from "../locales/en/status.json";
import de from "../locales/de/status.json";
import { localizeGenerated } from "./localized-text";

type Catalog = Record<string, unknown>;

function lookup(catalog: Catalog, key: string): string | undefined {
  let node: unknown = catalog;
  for (const part of key.split(".")) {
    if (typeof node !== "object" || node === null) return undefined;
    node = (node as Catalog)[part];
  }
  return typeof node === "string" ? node : undefined;
}

// A stand-in for i18next's t: {{name}} interpolation over one catalog.
function translator(catalog: Catalog) {
  const t = ((key: string, params?: Record<string, unknown>) =>
    (lookup(catalog, key) ?? key).replace(/\{\{(\w+)\}\}/g, (_, name) =>
      String(params?.[name] ?? ""),
    )) as unknown as TFunction;
  const exists = (key: string) => lookup(catalog, key) !== undefined;
  return { t, exists };
}

describe("localizeGenerated", () => {
  test("renders a templated title in the visitor's language", () => {
    const { t, exists } = translator(de);
    const out = localizeGenerated(
      t,
      exists,
      { key: "publication.openedTitle", params: { name: "Database" } },
      "Database is experiencing issues",
    );
    expect(out).toBe("Database hat Probleme");
  });

  test("stands in the group title when the component has no name", () => {
    const { t, exists } = translator(en);
    const out = localizeGenerated(
      t,
      exists,
      { key: "publication.resolvedTitle" },
      "fallback",
    );
    expect(out).toBe("Some services are experiencing issues has recovered");
  });

  test("falls back to the stored text without a recipe", () => {
    const { t, exists } = translator(de);
    expect(localizeGenerated(t, exists, undefined, "Written by hand")).toBe(
      "Written by hand",
    );
  });

  test("falls back on a key this build does not know", () => {
    const { t, exists } = translator(de);
    expect(
      localizeGenerated(t, exists, { key: "publication.fromTheFuture" }, "literal"),
    ).toBe("literal");
  });

  test("never renders a key outside publication.*", () => {
    const { t, exists } = translator(de);
    expect(
      localizeGenerated(t, exists, { key: "tv.ongoingFor" }, "literal"),
    ).toBe("literal");
  });
});
