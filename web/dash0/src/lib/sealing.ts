/**
 * Browser-sealed check secrets (spec 2026-11-08-01).
 *
 * The check form seals the secret map in the browser, for the X25519 keys
 * only the org's agents (or a shared region key) hold, and sends the age
 * ciphertext as `sealedSecrets`. The server never receives the plaintext.
 *
 * `age-encryption` (typage) is loaded with a dynamic import, so only the
 * pages that seal pay for it.
 */
import { apiFetch } from "@/api/client";

/** One key a blob must be sealed to, as /sealing-recipients returns it. */
export interface SealingRecipient {
  kind: "region" | "agent";
  region: string;
  agentUid?: string;
  agentName?: string;
  recipient: string;
  fingerprint: string;
}

/** The `sealedSecrets` field of a check create or PATCH. */
export interface SealedSecrets {
  keys: string[];
  fingerprints: string[];
  ct: string;
  /** The plaintext carries a `_binding` to the check's target (spec 2026-11-11-01). */
  bound?: boolean;
  /** The target list the binding was digested over (spec 2026-11-12-02). */
  targetFields?: string[];
}

/** Error code of a write sealed to a recipient set that is no longer current. */
export const SEALING_RECIPIENTS_CHANGED = "SEALING_RECIPIENTS_CHANGED";

/** Error code of a write that moves a sealed check's target without new secrets. */
export const SEALED_TARGET_CHANGED = "SEALED_TARGET_CHANGED";

/**
 * Target binding of sealed secrets (spec 2026-11-11-01), byte for byte the Go
 * `credentials` canonicaliser (binding.go), pinned by the shared fixture
 * `__fixtures__/target-binding.json`.
 *
 * The sealed plaintext carries a reserved `_binding`: the check type and the
 * SHA-256 of a canonical JSON of the type's target fields (url, host, port,
 * database, tunnel...). The agent refuses the secrets once one changes, so
 * whoever writes the stored config cannot send them to another host.
 */
export const BINDING_KEY = "_binding";

/** What the sealed secrets are bound to. */
export interface SealTarget {
  type: string;
  /** The type's `targetFields` (check-types metadata). */
  targetFields: readonly string[];
  /** The public config the check is saved with. */
  config: Record<string, unknown>;
}

const DEFAULT_URL_PORTS: Record<string, string> = {
  http: "80",
  https: "443",
  ws: "80",
  wss: "443",
  ftp: "21",
};

function asciiLower(value: string): string {
  return value.replace(/[A-Z]/g, (c) => c.toLowerCase());
}

/**
 * Lowercases a URL's scheme and host (ASCII) and makes the scheme's default
 * port explicit; the rest is kept as is. Not `scheme://…`: unchanged.
 */
export function normalizeTargetURL(raw: string): string {
  const sep = raw.indexOf("://");
  if (sep <= 0 || !/^[A-Za-z][A-Za-z0-9+.-]*$/.test(raw.slice(0, sep))) return raw;
  const scheme = asciiLower(raw.slice(0, sep));
  const rest = raw.slice(sep + 3);
  const endMatch = rest.search(/[/?#]/);
  const end = endMatch < 0 ? rest.length : endMatch;
  let authority = rest.slice(0, end);
  const tail = rest.slice(end);
  let userinfo = "";
  const at = authority.lastIndexOf("@");
  if (at >= 0) {
    userinfo = authority.slice(0, at + 1);
    authority = authority.slice(at + 1);
  }
  let host = authority;
  let port = "";
  if (authority.startsWith("[")) {
    const closing = authority.indexOf("]");
    if (closing >= 0) {
      host = authority.slice(0, closing + 1);
      const after = authority.slice(closing + 1);
      if (after.startsWith(":")) port = after.slice(1);
    }
  } else {
    const colon = authority.lastIndexOf(":");
    if (colon >= 0) {
      host = authority.slice(0, colon);
      port = authority.slice(colon + 1);
    }
  }
  if (port === "") port = DEFAULT_URL_PORTS[scheme] ?? "";
  return `${scheme}://${userinfo}${asciiLower(host)}${port ? `:${port}` : ""}${tail}`;
}

function canonicalString(value: string): string {
  let out = '"';
  for (const char of value) {
    const code = char.charCodeAt(0);
    if (char === '"' || char === "\\") out += `\\${char}`;
    else if (code < 0x20) out += `\\u00${code.toString(16).padStart(2, "0")}`;
    else out += char;
  }
  return `${out}"`;
}

function formatNumber(value: number): string {
  if (Number.isInteger(value) && Math.abs(value) < 1e21) return value.toFixed(0);
  return String(value);
}

/** The canonical JSON of a value, or null when it carries none. */
function canonicalValue(value: unknown): string | null {
  if (value === undefined || value === null || value === "") return null;
  if (typeof value === "string") return canonicalString(value);
  if (typeof value === "boolean") return canonicalString(String(value));
  if (typeof value === "number") return canonicalString(formatNumber(value));
  if (Array.isArray(value)) {
    return `[${value.map((item) => canonicalValue(item) ?? "null").join(",")}]`;
  }
  if (typeof value === "object") {
    const parts: string[] = [];
    for (const key of Object.keys(value as object).sort()) {
      const item = canonicalValue((value as Record<string, unknown>)[key]);
      if (item !== null) parts.push(`${canonicalString(key)}:${item}`);
    }
    return `{${parts.join(",")}}`;
  }
  return null;
}

function isURLField(field: string): boolean {
  return field === "url" || field.endsWith("Url") || field.endsWith("_url");
}

function isHostField(field: string): boolean {
  return field === "host" || field === "hostname" || field === "brokers";
}

function lowerStrings(value: unknown): unknown {
  if (typeof value === "string") return asciiLower(value);
  if (Array.isArray(value)) return value.map(lowerStrings);
  return value;
}

/**
 * The wildcard target field (spec 2026-11-12-02): every public key of the
 * config but NON_TARGET_KEYS. The target list of a type that declares none
 * (plugins), as published by `GET /check-types`.
 */
export const ALL_PUBLIC_KEYS = "*";

/** The keys the wildcard leaves out (the Go `credentials.NonTargetKeys`). */
export const NON_TARGET_KEYS: readonly string[] = ["timeout"];

function expandTargetFields(
  targetFields: readonly string[],
  config: Record<string, unknown>,
): string[] {
  const fields = new Set<string>();
  for (const field of targetFields) {
    if (field !== ALL_PUBLIC_KEYS) {
      fields.add(field);
      continue;
    }
    for (const key of Object.keys(config)) {
      if (!NON_TARGET_KEYS.includes(key)) fields.add(key);
    }
  }
  return [...fields].sort();
}

/** The canonical JSON the target digest is computed over. */
export function canonicalTarget(
  targetFields: readonly string[],
  config: Record<string, unknown>,
): string {
  const parts: string[] = [];
  for (const field of expandTargetFields(targetFields, config)) {
    let value = config[field];
    if (isURLField(field) && typeof value === "string" && value !== "") {
      value = normalizeTargetURL(value);
    } else if (isHostField(field)) {
      value = lowerStrings(value);
    }
    const canonical = canonicalValue(value);
    // An empty object (`env: {}`) carries no value, like an absent field.
    if (canonical !== null && canonical !== "{}") parts.push(`${canonicalString(field)}:${canonical}`);
  }
  return `{${parts.join(",")}}`;
}

/** Hex SHA-256 of the canonical target. */
export async function targetDigest(
  targetFields: readonly string[],
  config: Record<string, unknown>,
): Promise<string> {
  const digest = await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(canonicalTarget(targetFields, config)),
  );
  return toHex(new Uint8Array(digest));
}

/** Whether two public configs differ on a target field. */
export function targetChanged(
  targetFields: readonly string[],
  before: Record<string, unknown>,
  after: Record<string, unknown>,
): boolean {
  return canonicalTarget(targetFields, before) !== canonicalTarget(targetFields, after);
}

/**
 * Whether this page can seal: `crypto.subtle` (fingerprints) needs a secure
 * context (HTTPS or localhost).
 */
export function canSealInBrowser(): boolean {
  return (
    typeof window !== "undefined" &&
    window.isSecureContext === true &&
    typeof crypto !== "undefined" &&
    typeof crypto.subtle !== "undefined"
  );
}

function toHex(bytes: Uint8Array): string {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

function toBase64(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

/**
 * The recipient fingerprint, byte for byte the Go
 * `credentials.RecipientFingerprint`: first 8 bytes of SHA-256 of the
 * recipient string, hex.
 */
export async function recipientFingerprint(recipient: string): Promise<string> {
  const digest = await crypto.subtle.digest(
    "SHA-256",
    new TextEncoder().encode(recipient),
  );
  return toHex(new Uint8Array(digest).slice(0, 8));
}

/** The org's browser sealing mode (spec 2026-11-08-01 §1). */
export type BrowserSealingMode = "optional" | "required";

/** Fetches what a blob for these private regions must be sealed to, and the
 * org's mode. No region returns the mode only. */
export async function fetchRecipients(
  org: string,
  regions: string[],
): Promise<{ recipients: SealingRecipient[]; mode: BrowserSealingMode }> {
  const query = regions.length ? `?region=${encodeURIComponent(regions.join(","))}` : "";
  const response = await apiFetch<{
    data?: SealingRecipient[];
    browserSealingMode?: BrowserSealingMode;
  }>(`/api/v1/orgs/${org}/sealing-recipients${query}`);
  return {
    recipients: response.data ?? [],
    mode: response.browserSealingMode === "required" ? "required" : "optional",
  };
}

/**
 * Splits the browser-sealed secrets out of a check config: every declared
 * secret field carrying a value. HTTP's username/password are folded into
 * `basicAuth`, as the server does, so the username never leaves in plaintext
 * next to a sealed password.
 */
export function splitSecrets(
  type: string | undefined,
  config: Record<string, unknown>,
  secretFields: readonly string[],
): { publicConfig: Record<string, unknown>; secrets: Record<string, unknown> } {
  const publicConfig: Record<string, unknown> = { ...config };
  const secrets: Record<string, unknown> = {};
  const hasValue = (v: unknown) =>
    v !== undefined &&
    v !== null &&
    v !== "" &&
    !(typeof v === "object" && !Array.isArray(v) && Object.keys(v as object).length === 0);

  if ((type === "http" || type === "health") && secretFields.includes("basicAuth")) {
    const username = publicConfig.username;
    const password = publicConfig.password;
    if (hasValue(username) || hasValue(password)) {
      secrets.basicAuth = `${(username as string) ?? ""}:${(password as string) ?? ""}`;
    }
    delete publicConfig.username;
    delete publicConfig.password;
  }

  for (const key of secretFields) {
    if (key in secrets) continue;
    if (hasValue(publicConfig[key])) secrets[key] = publicConfig[key];
    if (key in publicConfig && hasValue(publicConfig[key])) delete publicConfig[key];
  }
  return { publicConfig, secrets };
}

/**
 * The config a form sends while its secret inputs are locked: every declared
 * secret key is dropped, set or not (plus HTTP's username/password). An empty
 * key (`secretHeaders: {}`, `password: ""`) would read as a clear on the
 * server and silently drop the stored secrets.
 */
export function withoutSecretKeys(
  type: string | undefined,
  config: Record<string, unknown>,
  secretFields: readonly string[],
): Record<string, unknown> {
  const out: Record<string, unknown> = { ...config };
  if ((type === "http" || type === "health") && secretFields.includes("basicAuth")) {
    delete out.username;
    delete out.password;
  }
  for (const key of secretFields) delete out[key];
  return out;
}

/**
 * Seals a secret map to every recipient. The plaintext is the JSON object of
 * the secrets, which the agent merges into the check config. With a target,
 * it also carries the `_binding` to the check's type and target fields
 * (spec 2026-11-11-01).
 */
export async function sealSecrets(
  recipients: Pick<SealingRecipient, "recipient">[],
  secrets: Record<string, unknown>,
  target?: SealTarget,
): Promise<SealedSecrets> {
  if (recipients.length === 0) {
    throw new Error("no recipient to seal to");
  }
  const { Encrypter } = await import("age-encryption");
  const encrypter = new Encrypter();
  for (const { recipient } of recipients) {
    encrypter.addRecipient(recipient);
  }
  const plain: Record<string, unknown> = { ...secrets };
  delete plain[BINDING_KEY];
  if (target) {
    plain[BINDING_KEY] = {
      type: target.type,
      digest: await targetDigest(target.targetFields, target.config),
    };
  }
  const ct = await encrypter.encrypt(JSON.stringify(plain));
  const fingerprints = await Promise.all(
    recipients.map(({ recipient }) => recipientFingerprint(recipient)),
  );
  return {
    keys: Object.keys(secrets)
      .filter((key) => key !== BINDING_KEY)
      .sort(),
    fingerprints: fingerprints.sort(),
    ct: toBase64(ct),
    ...(target ? { bound: true, targetFields: [...target.targetFields] } : {}),
  };
}

/** A freshly generated shared region key. */
export interface GeneratedRegionKey {
  /** "AGE-SECRET-KEY-1…": shown once, never sent to the server. */
  identity: string;
  /** "age1…": the only half the server receives. */
  recipient: string;
  fingerprint: string;
}

/** Generates a shared region key in the browser (spec 2026-11-08-01 §2). */
export async function generateRegionKey(): Promise<GeneratedRegionKey> {
  const { generateX25519Identity, identityToRecipient } = await import(
    "age-encryption"
  );
  const identity = await generateX25519Identity();
  const recipient = await identityToRecipient(identity);
  return { identity, recipient, fingerprint: await recipientFingerprint(recipient) };
}

/**
 * Whether a check with these regions can use browser encryption: every
 * region is a private one (`@…`).
 */
export function isBrowserSealingEligible(regions: string[]): boolean {
  return regions.length > 0 && regions.every((region) => region.startsWith("@"));
}

/** A pasted age X25519 recipient: `age1` and 58 bech32 characters (spec 2026-11-10-03 §3). */
export function isAgeRecipient(value: string): boolean {
  return /^age1[02-9ac-hj-np-z]{58}$/.test(value.trim());
}
