// Spec 2026-10-25-01: the eslint-suppressions.json baseline lets an existing
// violation pass, while the same line in a file that is not baselined fails.
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { ESLint } from "eslint";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import noTextLocators from "./no-text-locators.js";

const line = 'page.getByText("Save");\n';
const ruleId = "testid-local/no-text-locators";

let dir;

beforeAll(() => {
  dir = mkdtempSync(join(tmpdir(), "eslint-baseline-"));
  mkdirSync(join(dir, "e2e"));
  writeFileSync(join(dir, "e2e/old.spec.js"), line);
  writeFileSync(join(dir, "e2e/new.spec.js"), line);
  writeFileSync(
    join(dir, "eslint-suppressions.json"),
    JSON.stringify({ "e2e/old.spec.js": { [ruleId]: { count: 1 } } }),
  );
});

afterAll(() => rmSync(dir, { recursive: true, force: true }));

describe("eslint-suppressions.json baseline", () => {
  it("passes the baselined file and fails the same line in a new file", async () => {
    const eslint = new ESLint({
      cwd: dir,
      overrideConfigFile: true,
      overrideConfig: [
        {
          files: ["e2e/**/*.js"],
          languageOptions: { ecmaVersion: 2022, sourceType: "module" },
          plugins: {
            "testid-local": { rules: { "no-text-locators": noTextLocators } },
          },
          rules: { [ruleId]: "error" },
        },
      ],
      applySuppressions: true,
      suppressionsLocation: join(dir, "eslint-suppressions.json"),
    });

    const results = await eslint.lintFiles([
      "e2e/old.spec.js",
      "e2e/new.spec.js",
    ]);
    const byFile = Object.fromEntries(
      results.map((r) => [
        r.filePath.slice(dir.length + 1),
        r.messages.map((m) => m.ruleId),
      ]),
    );

    expect(byFile["e2e/old.spec.js"]).toEqual([]);
    expect(byFile["e2e/new.spec.js"]).toEqual([ruleId]);
  });
});
