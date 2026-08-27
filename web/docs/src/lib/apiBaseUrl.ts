/**
 * Base URL resolution for the generated API reference (`/docs/api/*`).
 *
 * The reference pages are generated at build time by
 * `docusaurus-plugin-openapi-docs`, so the `servers:` list of
 * `server/internal/app/openapi/openapi.yaml` is baked into every page. Without
 * this resolver a reader on a self-hosted instance is shown
 * `https://solidping.io` and copies examples aimed at a host they do not use.
 *
 * The obvious fix — always use `window.location.origin`, the way the Scalar
 * explorer does in `server/internal/app/openapi/index.html` — is wrong on the
 * documentation host. `handlerWithDocsHost` (`server/internal/app/server.go`)
 * redirects every non-`/docs` path on that host into `/docs`, so
 * `docs.solidping.io/api/v1/...` never reaches an API at all. The docs host is
 * therefore the one place where the spec's own first server (the cloud) is the
 * correct answer.
 *
 * Resolution order:
 *   1. current origin is a known docs host → the spec's list, untouched;
 *   2. otherwise                          → the current origin, first.
 *
 * Everything here is pure: no React, no Docusaurus, no `window`. The caller
 * supplies the origin. See `apiBaseUrl.test.ts`.
 */

/** A single entry of an OpenAPI `servers:` list, as the theme models it. */
export interface ApiServer {
  url: string;
  description?: string;
  variables?: Record<
    string,
    { enum?: string[]; default: string; description?: string }
  >;
}

/** Description attached to the injected "current instance" entry. */
export const CURRENT_INSTANCE_DESCRIPTION = "This SolidPing instance";

/**
 * Hostname of an origin or a bare host, lowercased and without its port.
 *
 * Mirrors `docsHostMatches` in `server/internal/app/server.go`, which compares
 * `net.SplitHostPort(req.Host)` case-insensitively — the two must agree, or the
 * browser and the server would disagree on what a docs host is. Returns "" for
 * anything unparseable, which callers treat as "unknown host".
 */
export function hostnameOf(originOrHost: string): string {
  const raw = (originOrHost ?? "").trim();

  if (raw === "") {
    return "";
  }

  for (const candidate of [raw, `https://${raw}`]) {
    try {
      const { hostname } = new URL(candidate);

      if (hostname !== "") {
        return hostname.toLowerCase();
      }
    } catch {
      // Not a URL with this shape; try the next candidate.
    }
  }

  return "";
}

/**
 * Whether `origin` is one of the hosts that serve the documentation but not the
 * API. An empty or missing list means "no docs host is known", i.e. every host
 * is treated as an instance.
 *
 * KNOWN DIVERGENCE from `docsHostMatches` (server/internal/app/server.go), not
 * enforced by any code, hence this note. Go strips the port from the *request*
 * host but compares the *configured* host raw; we strip the port from both. So
 * a docs host configured with a port — `SP_DOCS_HOST="docs.acme.com:8443"` —
 * never matches server-side (that host is not redirected, and does serve the
 * API), while this function calls it a docs host and hands back the cloud URL.
 * We are the lenient side. Hostname-only is the semantics this bundle wants and
 * matches the documented config shape (a bare hostname), so the fix belongs on
 * the Go side, where it would change which requests get redirected — a server
 * behaviour change that needs its own spec. If you touch either function, read
 * the other.
 */
export function isKnownDocsHost(
  origin: string,
  docsHosts: readonly string[] | undefined,
): boolean {
  const host = hostnameOf(origin);

  if (host === "" || !docsHosts || docsHosts.length === 0) {
    return false;
  }

  return docsHosts.some((docsHost) => hostnameOf(docsHost) === host);
}

/** Trailing slashes are cosmetic; ignore them when comparing server URLs. */
function normalizeUrl(url: string): string {
  return (url ?? "").trim().replace(/\/+$/, "");
}

/**
 * The ordered list of servers the Base URL control should offer, most-likely
 * first.
 *
 * On a docs host the spec's list is returned untouched — the origin is never
 * injected, because that host redirects API paths into the documentation site.
 * Everywhere else the current origin comes first and the spec's own entries are
 * kept after it, so a reader can still pick `localhost` deliberately. If the
 * origin is already one of the declared servers it is moved to the front rather
 * than duplicated.
 *
 * Pure, non-mutating and idempotent: feeding the result back in returns an
 * equivalent list, which is what lets the caller stop re-applying it.
 */
export function resolveApiServers(
  origin: string,
  docsHosts: readonly string[] | undefined,
  specServers: readonly ApiServer[] | undefined,
): ApiServer[] {
  const declared = [...(specServers ?? [])];

  if (hostnameOf(origin) === "" || isKnownDocsHost(origin, docsHosts)) {
    return declared;
  }

  const originUrl = normalizeUrl(origin);
  const alreadyDeclared = declared.find(
    (server) => normalizeUrl(server.url) === originUrl,
  );

  if (alreadyDeclared) {
    return [
      alreadyDeclared,
      ...declared.filter((server) => server !== alreadyDeclared),
    ];
  }

  return [
    { url: originUrl, description: CURRENT_INSTANCE_DESCRIPTION },
    ...declared,
  ];
}

/**
 * The single base URL the reference should show: `(origin, docsHosts,
 * specServers) → chosen base URL`. Empty when there is nothing to choose from.
 */
export function resolveApiBaseUrl(
  origin: string,
  docsHosts: readonly string[] | undefined,
  specServers: readonly ApiServer[] | undefined,
): string {
  return resolveApiServers(origin, docsHosts, specServers)[0]?.url ?? "";
}

/**
 * Whether the host-resolved default should be (re-)selected, or the reader's
 * own pick left alone.
 *
 * The theme persists the selected server and restores it on the next page, but
 * it persists its *own* automatic default the same way it persists a click — so
 * "something is stored" cannot distinguish the two. `autoSelectedUrl` is the
 * value we last defaulted to, remembered separately by the caller: if the
 * restored selection is still that value, it is ours to refresh; if it differs,
 * the reader chose it and we leave it.
 *
 * @param currentUrl      the currently selected server URL, if any
 * @param autoSelectedUrl the URL this resolver last selected on its own
 */
export function shouldApplyDefaultSelection(
  currentUrl: string | undefined,
  autoSelectedUrl: string | null | undefined,
): boolean {
  if (!currentUrl) {
    return true; // Nothing selected yet.
  }

  if (!autoSelectedUrl) {
    return true; // We never defaulted here, so this is the theme's own default.
  }

  return currentUrl === autoSelectedUrl;
}

/** Whether two server lists carry the same URLs in the same order. */
export function sameServerUrls(
  a: readonly ApiServer[],
  b: readonly ApiServer[],
): boolean {
  return (
    a.length === b.length &&
    a.every((server, index) => server.url === b[index]?.url)
  );
}

/**
 * Reads the build-time `customFields.docsHosts` value (see
 * `docusaurus.config.ts`). Accepts an array or a comma-separated string so the
 * env-var override needs no parsing at the call site.
 */
export function docsHostsFrom(customFields: unknown): string[] {
  const raw = (customFields as { docsHosts?: unknown } | undefined)?.docsHosts;

  const values = Array.isArray(raw)
    ? raw
    : typeof raw === "string"
      ? raw.split(",")
      : [];

  return values
    .filter((value): value is string => typeof value === "string")
    .map((value) => value.trim())
    .filter((value) => value !== "");
}
