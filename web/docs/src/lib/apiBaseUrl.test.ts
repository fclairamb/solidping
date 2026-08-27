import { describe, expect, test } from "bun:test";

import {
  CURRENT_INSTANCE_DESCRIPTION,
  docsHostsFrom,
  hostnameOf,
  isKnownDocsHost,
  resolveApiBaseUrl,
  resolveApiServers,
  sameServerUrls,
  shouldApplyDefaultSelection,
  type ApiServer,
} from "./apiBaseUrl";

/** The `servers:` list baked into every generated reference page. */
const SPEC_SERVERS: ApiServer[] = [
  { url: "https://solidping.io", description: "SolidPing cloud" },
  { url: "http://localhost:4000", description: "Local development server" },
];

const DOCS_HOSTS = ["docs.solidping.io"];

describe("hostnameOf", () => {
  test.each([
    ["https://docs.solidping.io", "docs.solidping.io"],
    ["https://docs.solidping.io:8443", "docs.solidping.io"],
    ["https://DOCS.SolidPing.IO", "docs.solidping.io"],
    ["http://localhost:4000", "localhost"],
    ["docs.solidping.io", "docs.solidping.io"],
    ["docs.solidping.io:8443", "docs.solidping.io"],
    ["  https://docs.solidping.io  ", "docs.solidping.io"],
    ["", ""],
  ])("%s -> %s", (input, expected) => {
    expect(hostnameOf(input)).toBe(expected);
  });
});

describe("isKnownDocsHost", () => {
  test("matches on hostname only, ignoring scheme, port and case", () => {
    expect(isKnownDocsHost("https://docs.solidping.io", DOCS_HOSTS)).toBe(true);
    expect(isKnownDocsHost("https://DOCS.solidping.io:8443", DOCS_HOSTS)).toBe(
      true,
    );
    // Deliberately lenient on a *configured* host carrying a port: Go's
    // docsHostMatches compares the configured value raw and would NOT match
    // here. See the divergence note on isKnownDocsHost — hostname-only is the
    // semantics this bundle wants, and the Go side is the one to change.
    expect(
      isKnownDocsHost("http://docs.solidping.io", ["DOCS.SolidPing.io:443"]),
    ).toBe(true);
  });

  test("an instance host is not a docs host", () => {
    expect(isKnownDocsHost("https://monitor.acme.com", DOCS_HOSTS)).toBe(false);
    // Not a suffix match: a lookalike host must not be swallowed.
    expect(isKnownDocsHost("https://notdocs.solidping.io", DOCS_HOSTS)).toBe(
      false,
    );
    expect(isKnownDocsHost("https://solidping.io", DOCS_HOSTS)).toBe(false);
  });

  test("an empty or missing list means no host is a docs host", () => {
    expect(isKnownDocsHost("https://docs.solidping.io", [])).toBe(false);
    expect(isKnownDocsHost("https://docs.solidping.io", undefined)).toBe(false);
  });
});

describe("resolveApiBaseUrl", () => {
  // Positive control. Without this case a resolver that unconditionally
  // returned the cloud would pass every docs-host assertion below.
  test("a self-hosted instance host wins over the spec's list", () => {
    expect(
      resolveApiBaseUrl("https://monitor.acme.com", DOCS_HOSTS, SPEC_SERVERS),
    ).toBe("https://monitor.acme.com");
  });

  test("a port and a non-https scheme are preserved on an instance host", () => {
    expect(
      resolveApiBaseUrl("http://10.0.0.7:4000", DOCS_HOSTS, SPEC_SERVERS),
    ).toBe("http://10.0.0.7:4000");
  });

  test("a docs host falls back to the spec's first server, never its own origin", () => {
    const origins = [
      "https://docs.solidping.io",
      "https://DOCS.solidping.io",
      "https://docs.solidping.io:8443",
    ];

    for (const origin of origins) {
      expect(resolveApiBaseUrl(origin, DOCS_HOSTS, SPEC_SERVERS)).toBe(
        "https://solidping.io",
      );
    }
  });

  test("an unknown host falls back to its own origin", () => {
    expect(
      resolveApiBaseUrl("https://ping.acme.internal", DOCS_HOSTS, SPEC_SERVERS),
    ).toBe("https://ping.acme.internal");
  });

  test("an empty or missing docsHosts behaves like 'not a docs host'", () => {
    expect(
      resolveApiBaseUrl("https://docs.solidping.io", [], SPEC_SERVERS),
    ).toBe("https://docs.solidping.io");
    expect(
      resolveApiBaseUrl("https://docs.solidping.io", undefined, SPEC_SERVERS),
    ).toBe("https://docs.solidping.io");
  });

  test("an unusable origin leaves the spec's list alone", () => {
    expect(resolveApiBaseUrl("", DOCS_HOSTS, SPEC_SERVERS)).toBe(
      "https://solidping.io",
    );
  });

  test("nothing to choose from yields an empty string", () => {
    expect(resolveApiBaseUrl("https://docs.solidping.io", DOCS_HOSTS, [])).toBe(
      "",
    );
    expect(
      resolveApiBaseUrl("https://docs.solidping.io", DOCS_HOSTS, undefined),
    ).toBe("");
  });
});

describe("resolveApiServers", () => {
  test("the docs origin is never offered at all on a docs host", () => {
    const resolved = resolveApiServers(
      "https://docs.solidping.io",
      DOCS_HOSTS,
      SPEC_SERVERS,
    );

    expect(
      resolved.some((server) => hostnameOf(server.url) === "docs.solidping.io"),
    ).toBe(false);
    // The spec's own entries stay selectable — including localhost.
    expect(resolved.map((server) => server.url)).toEqual([
      "https://solidping.io",
      "http://localhost:4000",
    ]);
  });

  test("the spec's entries stay available behind the instance origin", () => {
    const resolved = resolveApiServers(
      "https://monitor.acme.com",
      DOCS_HOSTS,
      SPEC_SERVERS,
    );

    expect(resolved.map((server) => server.url)).toEqual([
      "https://monitor.acme.com",
      "https://solidping.io",
      "http://localhost:4000",
    ]);
    expect(resolved[0]?.description).toBe(CURRENT_INSTANCE_DESCRIPTION);
  });

  test("an origin already declared by the spec is moved, not duplicated", () => {
    const resolved = resolveApiServers(
      "http://localhost:4000/",
      DOCS_HOSTS,
      SPEC_SERVERS,
    );

    expect(resolved.map((server) => server.url)).toEqual([
      "http://localhost:4000",
      "https://solidping.io",
    ]);
    // The spec's own description survives — no synthetic entry was added.
    expect(resolved[0]?.description).toBe("Local development server");
  });

  test("the input list is never mutated", () => {
    const input: ApiServer[] = [...SPEC_SERVERS];

    resolveApiServers("https://monitor.acme.com", DOCS_HOSTS, input);

    expect(input).toEqual(SPEC_SERVERS);
  });

  test("re-resolving its own output is a no-op", () => {
    for (const origin of ["https://monitor.acme.com", "https://docs.solidping.io"]) {
      const once = resolveApiServers(origin, DOCS_HOSTS, SPEC_SERVERS);
      const twice = resolveApiServers(origin, DOCS_HOSTS, once);

      expect(sameServerUrls(twice, once)).toBe(true);
    }
  });
});

describe("sameServerUrls", () => {
  test("compares URLs in order", () => {
    expect(sameServerUrls(SPEC_SERVERS, [...SPEC_SERVERS])).toBe(true);
    expect(sameServerUrls(SPEC_SERVERS, [...SPEC_SERVERS].reverse())).toBe(
      false,
    );
    expect(sameServerUrls(SPEC_SERVERS, SPEC_SERVERS.slice(0, 1))).toBe(false);
  });
});

describe("docsHostsFrom", () => {
  test.each([
    [{ docsHosts: ["docs.solidping.io"] }, ["docs.solidping.io"]],
    [
      { docsHosts: "docs.solidping.io, docs.acme.com" },
      ["docs.solidping.io", "docs.acme.com"],
    ],
    [{ docsHosts: [] }, []],
    [{ docsHosts: "" }, []],
    [{}, []],
    [undefined, []],
    [{ docsHosts: 42 }, []],
  ])("%p -> %p", (customFields, expected) => {
    expect(docsHostsFrom(customFields)).toEqual(expected);
  });
});

describe("shouldApplyDefaultSelection", () => {
  test("applies when nothing is selected yet", () => {
    expect(shouldApplyDefaultSelection(undefined, null)).toBe(true);
    expect(shouldApplyDefaultSelection("", null)).toBe(true);
  });

  test("applies when the selection is the theme's own default, not ours", () => {
    // First visit: the theme selected the spec's first server during render and
    // persisted it, but we have never defaulted here.
    expect(shouldApplyDefaultSelection("https://solidping.io", null)).toBe(true);
    expect(shouldApplyDefaultSelection("https://solidping.io", undefined)).toBe(
      true,
    );
  });

  test("re-applies its own previous default", () => {
    expect(
      shouldApplyDefaultSelection(
        "https://monitor.acme.com",
        "https://monitor.acme.com",
      ),
    ).toBe(true);
  });

  test("leaves a deliberate pick alone", () => {
    // We defaulted to the instance; the reader then picked the cloud.
    expect(
      shouldApplyDefaultSelection(
        "https://solidping.io",
        "https://monitor.acme.com",
      ),
    ).toBe(false);
    expect(
      shouldApplyDefaultSelection(
        "http://localhost:4000",
        "https://monitor.acme.com",
      ),
    ).toBe(false);
  });
});
