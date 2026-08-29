import { describe, expect, test } from "bun:test";

import { SCOPE_LABELS, transformBullet, transformChangelog, transformHeading } from "./changelog";

describe("transformHeading", () => {
  test("parses a release-please heading into version/date/anchor/diffUrl", () => {
    expect(
      transformHeading(
        "## [0.20.0](https://github.com/fclairamb/solidping/compare/v0.19.1...v0.20.0) (2026-08-29)",
      ),
    ).toEqual({
      version: "0.20.0",
      date: "2026-08-29",
      anchor: "0200",
      diffUrl: "https://github.com/fclairamb/solidping/compare/v0.19.1...v0.20.0",
    });
  });

  test("an unrecognized heading shape returns null (caller keeps it verbatim)", () => {
    expect(transformHeading("## Unreleased")).toBeNull();
    expect(transformHeading("## [0.20.0](url) 2026-08-29")).toBeNull();
  });
});

describe("transformBullet — reference clutter", () => {
  test("a single PR + commit-hash pair collapses to one PR link", () => {
    const input =
      "* **auth:** registering with a too-short password returns 400 not 500 ([#282](https://github.com/fclairamb/solidping/issues/282)) ([289478e](https://github.com/fclairamb/solidping/commit/289478e7ee97a156a7747591bb6b94cdbac9a4c6))";

    expect(transformBullet(input)).toBe(
      "* **Authentication:** registering with a too-short password returns 400 not 500 ([#282](https://github.com/fclairamb/solidping/issues/282))",
    );
  });

  test("a multi-pair-link bullet with a shared group collapses to the distinct PR numbers, drops all hashes", () => {
    // Mirrors the real shape release-please produces for a backported fix:
    // the second PR shares a parenthesized group with the first commit hash
    // rather than getting its own enclosing parens.
    const input =
      "* **api:** update go dependencies (non-major) ([#279](https://github.com/fclairamb/solidping/issues/279)) ([4cbcc29](https://github.com/fclairamb/solidping/commit/4cbcc29206ee201276f15f01b695b43c19bbf381), [#283](https://github.com/fclairamb/solidping/issues/283)) ([9709152](https://github.com/fclairamb/solidping/commit/970915284d9af2b905439e45f8ec5aa8818b03d1))";

    expect(transformBullet(input)).toBe(
      "* **API:** update go dependencies (non-major) ([#279](https://github.com/fclairamb/solidping/issues/279), [#283](https://github.com/fclairamb/solidping/issues/283))",
    );
  });

  test("groups separated by a comma (not just whitespace) are still stripped in full", () => {
    // Real shape seen in CHANGELOG.md: the comma falls *between* two
    // parenthesized groups rather than between items inside one group.
    const input =
      "* **auth:** custom domains are now single-CNAME ([#170](https://github.com/fclairamb/solidping/issues/170)) ([1184b15](https://github.com/fclairamb/solidping/commit/1184b156c27192e2a67f9cc9842b446116e2d9e3)), ([#175](https://github.com/fclairamb/solidping/issues/175)) ([9ffd436](https://github.com/fclairamb/solidping/commit/9ffd436a4e40f5968439a425824c0525784851d8))";

    expect(transformBullet(input)).toBe(
      "* **Authentication:** custom domains are now single-CNAME ([#170](https://github.com/fclairamb/solidping/issues/170), [#175](https://github.com/fclairamb/solidping/issues/175))",
    );
  });

  test("a repeated PR number across pairs is de-duplicated to a single link", () => {
    const input =
      "* **checks:** did a thing ([#100](https://github.com/fclairamb/solidping/issues/100)) ([abc1234](https://github.com/fclairamb/solidping/commit/abc1234def), [#100](https://github.com/fclairamb/solidping/issues/100))";

    expect(transformBullet(input)).toBe(
      "* **Checks:** did a thing ([#100](https://github.com/fclairamb/solidping/issues/100))",
    );
  });

  test("a bullet with no scope prefix keeps its ref, drops the hash", () => {
    const input =
      "* status lifecycle improvements and created/running result handling ([#5](https://github.com/fclairamb/solidping/issues/5)) ([fc64c7d](https://github.com/fclairamb/solidping/commit/fc64c7d87c25f2b6be0f9722f46e024b2c64ca1b))";

    expect(transformBullet(input)).toBe(
      "* status lifecycle improvements and created/running result handling ([#5](https://github.com/fclairamb/solidping/issues/5))",
    );
  });

  test("a deps-scoped bullet is dropped entirely", () => {
    const input =
      "* **deps:** update dependency i18next to v26 ([#26](https://github.com/fclairamb/solidping/issues/26)) ([d5d1b7e](https://github.com/fclairamb/solidping/commit/d5d1b7edceec47fdc51eec2f4bad16e138906108))";

    expect(transformBullet(input)).toBeNull();
  });
});

describe("transformBullet — scope labels", () => {
  test("known scopes map to their product-facing name", () => {
    expect(SCOPE_LABELS.dash0).toBe("Dashboard");
    expect(SCOPE_LABELS.sftp).toBe("SFTP checks");
    expect(SCOPE_LABELS["status pages"]).toBe("Status pages");

    expect(transformBullet("* **dash0:** fixed a thing")).toBe(
      "* **Dashboard:** fixed a thing",
    );
    expect(transformBullet("* **status pages:** fixed a thing")).toBe(
      "* **Status pages:** fixed a thing",
    );
  });

  test("an unknown scope passes through unchanged rather than failing", () => {
    expect(transformBullet("* **some-brand-new-scope:** fixed a thing")).toBe(
      "* **some-brand-new-scope:** fixed a thing",
    );
  });
});

describe("transformBullet — robustness", () => {
  test("MDX-hostile characters in the body survive verbatim", () => {
    const input =
      "* **checks:** the `<name>` field and a stray `{brace}` are now validated ([#42](https://github.com/fclairamb/solidping/issues/42)) ([deadbee](https://github.com/fclairamb/solidping/commit/deadbeefcafefeed))";

    expect(transformBullet(input)).toBe(
      "* **Checks:** the `<name>` field and a stray `{brace}` are now validated ([#42](https://github.com/fclairamb/solidping/issues/42))",
    );
  });

  test("a non-bullet line is returned untouched", () => {
    expect(transformBullet("not a bullet at all")).toBe("not a bullet at all");
  });
});

describe("transformChangelog", () => {
  test("a deps-only release keeps its heading with a single Maintenance release line", () => {
    const input = `# Changelog

## [0.16.1](https://github.com/fclairamb/solidping/compare/v0.16.0...v0.16.1) (2026-08-16)


### Bug Fixes

* **deps:** update dependency foo to v2 ([#10](https://github.com/fclairamb/solidping/issues/10)) ([aaa1111](https://github.com/fclairamb/solidping/commit/aaa1111bbb2222ccc3333ddd4444eee5555fff6))
`;

    const expected = `## 0.16.1 — 2026-08-16 {#0161}

([diff](https://github.com/fclairamb/solidping/compare/v0.16.0...v0.16.1))

Maintenance release.
`;

    expect(transformChangelog(input)).toBe(expected);
  });

  test("a mixed release drops the deps bullets and the now-empty Bug Fixes section stays only if non-deps entries remain", () => {
    const input = `# Changelog

## [1.2.0](https://github.com/fclairamb/solidping/compare/v1.1.0...v1.2.0) (2026-08-20)


### Features

* **dash0:** a new widget ([#20](https://github.com/fclairamb/solidping/issues/20)) ([bbb2222](https://github.com/fclairamb/solidping/commit/bbb22223333444455556666777788889999aaaa))


### Bug Fixes

* **deps:** bump lib to v3 ([#21](https://github.com/fclairamb/solidping/issues/21)) ([ccc3333](https://github.com/fclairamb/solidping/commit/ccc33334444555566667777888899990000aaaa))
`;

    const expected = `## 1.2.0 — 2026-08-20 {#120}

([diff](https://github.com/fclairamb/solidping/compare/v1.1.0...v1.2.0))

### Features

* **Dashboard:** a new widget ([#20](https://github.com/fclairamb/solidping/issues/20))
`;

    expect(transformChangelog(input)).toBe(expected);
  });

  test("multiple versions are each transformed and separated by a blank line", () => {
    const input = `# Changelog

## [1.0.1](https://github.com/fclairamb/solidping/compare/v1.0.0...v1.0.1) (2026-08-10)


### Bug Fixes

* **api:** fix a thing ([#1](https://github.com/fclairamb/solidping/issues/1)) ([1111111](https://github.com/fclairamb/solidping/commit/11111112222333344445555666677778888aaaa))

## [1.0.0](https://github.com/fclairamb/solidping/compare/v0.9.0...v1.0.0) (2026-08-01)


### Features

* first release ([#0](https://github.com/fclairamb/solidping/issues/0)) ([0000000](https://github.com/fclairamb/solidping/commit/00000001111122223333444455556666aaaabbbb))
`;

    const output = transformChangelog(input);

    expect(output).toContain("## 1.0.1 — 2026-08-10 {#101}");
    expect(output).toContain("## 1.0.0 — 2026-08-01 {#100}");
    expect(output.indexOf("## 1.0.1")).toBeLessThan(output.indexOf("## 1.0.0"));
  });

  test("an unparseable heading is passed through verbatim instead of failing the build", () => {
    const input = `# Changelog

## Unreleased

Something is being worked on, format not yet decided <weird> & {odd}.

## [1.0.0](https://github.com/fclairamb/solidping/compare/v0.9.0...v1.0.0) (2026-08-01)


### Features

* **api:** a feature ([#1](https://github.com/fclairamb/solidping/issues/1)) ([1111111](https://github.com/fclairamb/solidping/commit/11111112222333344445555666677778888aaaa))
`;

    const output = transformChangelog(input);

    expect(output).toContain("## Unreleased");
    expect(output).toContain("Something is being worked on, format not yet decided <weird> & {odd}.");
    expect(output).toContain("## 1.0.0 — 2026-08-01 {#100}");
  });

  test("a file with no recognizable version heading is passed through as-is", () => {
    const input = "# Changelog\n\nNothing here yet.\n";
    expect(transformChangelog(input)).toBe(input.trim());
  });
});
