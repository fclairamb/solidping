// Builds a copy-pasteable curl command that reproduces what an HTTP check's
// probe sends (spec 2026-10-12-10). Pure and client-side: the check read API
// already strips every secret, so nothing sensitive can end up in the output.
// Secrets become shell-variable placeholders or a comment, never values.
//
// Mirrors server/internal/checkers/checkhttp/checker.go: GET by default, the
// body is sent for any method when non-empty, up to 10 redirects are followed.

const MAX_REDIRECTS = 10;
const PLACEHOLDER_NOTE =
  "# ${param:…} and ${env:…} references are not resolved. Replace them before running.";
const SECRET_HEADERS_NOTE =
  "# This check also sends secret headers that are not shown. Add each one with -H 'Name: value'.";
const SAME_HOST_NOTE =
  "# redirectHostPolicy: same-host has no curl equivalent: redirects to other hosts are still followed.";

const REFERENCE_RE = /\$\{(?:param|env):/;

function quote(value: string): string {
  return `'${value.replace(/'/g, "'\\''")}'`;
}

function asString(value: unknown): string {
  return typeof value === "string" ? value : "";
}

const UNIT_SECONDS: Record<string, number> = {
  ns: 1e-9,
  us: 1e-6,
  "µs": 1e-6,
  "μs": 1e-6,
  ms: 1e-3,
  s: 1,
  m: 60,
  h: 3600,
};

/** Parses a Go duration string ("1m30s", "500ms") into seconds, or null. */
export function parseGoDurationSeconds(input: unknown): number | null {
  if (typeof input !== "string" || input === "") return null;
  const re = /(\d+(?:\.\d+)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)/gy;
  let total = 0;
  let consumed = 0;
  let match: RegExpExecArray | null;
  while ((match = re.exec(input)) !== null) {
    total += parseFloat(match[1]) * UNIT_SECONDS[match[2]];
    consumed = re.lastIndex;
  }
  if (consumed !== input.length) return null;
  return Math.round(total * 1e6) / 1e6;
}

function plainHeaders(config: Record<string, unknown>): [string, string][] {
  const raw = config.headers;
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return [];
  return Object.entries(raw as Record<string, unknown>)
    .filter(([, v]) => typeof v === "string")
    .map(([k, v]) => [k, v as string] as [string, string])
    .sort(([a], [b]) => a.localeCompare(b));
}

/** True when the command contains a placeholder or note the operator must act on. */
export function curlCommandHasPlaceholders(
  config: Record<string, unknown>,
  configPrivateKeys: string[] = [],
): boolean {
  return (
    configPrivateKeys.includes("basicAuth") ||
    (asString(config.username) !== "" && configPrivateKeys.includes("password")) ||
    configPrivateKeys.includes("secretHeaders")
  );
}

export function buildCurlCommand(
  config: Record<string, unknown>,
  configPrivateKeys: string[] = [],
): string {
  const comments: string[] = [];
  const method = (asString(config.method) || "GET").toUpperCase();
  const isHead = method === "HEAD";
  const headers = plainHeaders(config);
  const body = isHead ? "" : asString(config.body);
  const url = asString(config.url);

  const flags: string[] = [];
  flags.push(isHead ? "curl --head" : "curl -i");
  if (!isHead && (method !== "GET" || body !== "")) flags.push(`-X ${method}`);
  for (const [name, value] of headers) flags.push(`-H ${quote(`${name}: ${value}`)}`);

  if (configPrivateKeys.includes("basicAuth")) {
    flags.push('-u "$SOLIDPING_BASIC_AUTH"');
  } else if (asString(config.username) !== "" && configPrivateKeys.includes("password")) {
    flags.push(`-u ${quote(`${asString(config.username)}:`)}"$SOLIDPING_BASIC_PASSWORD"`);
  }

  if (body !== "") flags.push(`--data-raw ${quote(body)}`);
  if (config.followRedirects !== false) flags.push(`-L --max-redirs ${MAX_REDIRECTS}`);
  if (config.verifySsl === false) flags.push("-k");

  const version = String(config.httpVersion ?? "");
  if (version === "2") {
    // The checker speaks h2c with prior knowledge on a cleartext URL.
    flags.push(url.startsWith("http://") ? "--http2-prior-knowledge" : "--http2");
  } else if (version === "3") {
    flags.push("--http3");
  }

  const seconds = parseGoDurationSeconds(config.timeout);
  if (seconds !== null) flags.push(`--max-time ${seconds}`);

  flags.push(quote(url));

  if (configPrivateKeys.includes("secretHeaders")) comments.push(SECRET_HEADERS_NOTE);
  if (config.redirectHostPolicy === "same-host") comments.push(SAME_HOST_NOTE);
  const hasReference =
    [url, body, ...headers.flat()].some((v) => REFERENCE_RE.test(v)) ||
    REFERENCE_RE.test(asString(config.username));
  if (hasReference) comments.push(PLACEHOLDER_NOTE);

  return [...comments, flags.join(" \\\n  ")].join("\n");
}
