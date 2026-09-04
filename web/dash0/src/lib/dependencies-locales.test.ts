import { describe, expect, it } from "vitest";

import depsDe from "@/locales/de/dependencies.json";
import depsEn from "@/locales/en/dependencies.json";
import depsEs from "@/locales/es/dependencies.json";
import depsFr from "@/locales/fr/dependencies.json";

// Dependencies are now edited in ONE place — the check form's Dependencies
// section (components/checks/form/sections/dependencies.tsx) — and merely
// displayed on the check detail card. That section used to be the only
// untranslated form section; it now renders entirely off `dependencies:*`
// keys, so a key missing from any locale would show a raw dotted path on the
// only screen where a dependency can be created or retuned. All four locales
// must carry every key below, as real non-empty strings.

const DEPENDENCY_LOCALES = [
  ["en", depsEn],
  ["fr", depsFr],
  ["de", depsDe],
  ["es", depsEs],
] as const;

const DEPENDENCY_KEYS = [
  "title",
  "dependsOn",
  "dependsOnHelp",
  "dependedOnBy",
  "dependedOnByHelp",
  "addDependency",
  "kind",
  "kindHard",
  "kindSoft",
  "kindHardTooltip",
  "kindSoftTooltip",
  "description",
  "descriptionPlaceholder",
  "noDependencies",
  "noParents",
  "noDependents",
  "unknownCheck",
  "editDependencies",
  "remove",
  "pickCheck",
  "searchChecks",
  "errors.cycle",
  "errors.duplicate",
  "errors.notFound",
  "errors.generic",
];

// Keys are looked up by walking segments, the way i18next resolves a dotted
// path.
function lookup(bundle: unknown, path: string): unknown {
  return path
    .split(".")
    .reduce<unknown>(
      (node, segment) =>
        node && typeof node === "object"
          ? (node as Record<string, unknown>)[segment]
          : undefined,
      bundle,
    );
}

describe("dependency editor copy locale parity", () => {
  it.each(DEPENDENCY_LOCALES)(
    "%s carries every dependency-editor key as a non-empty string",
    (_locale, bundle) => {
      const missing = DEPENDENCY_KEYS.filter((key) => {
        const value = lookup(bundle, key);
        return typeof value !== "string" || value.trim() === "";
      });
      expect(missing).toEqual([]);
    },
  );

  it("keeps the interpolation placeholder on the cycle error in every locale", () => {
    for (const [, bundle] of DEPENDENCY_LOCALES) {
      expect(lookup(bundle, "errors.cycle")).toContain("{{path}}");
    }
  });
});
