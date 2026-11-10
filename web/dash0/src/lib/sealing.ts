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
}

/** Error code of a write sealed to a recipient set that is no longer current. */
export const SEALING_RECIPIENTS_CHANGED = "SEALING_RECIPIENTS_CHANGED";

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
 * the secrets, which the agent merges into the check config.
 */
export async function sealSecrets(
  recipients: Pick<SealingRecipient, "recipient">[],
  secrets: Record<string, unknown>,
): Promise<SealedSecrets> {
  if (recipients.length === 0) {
    throw new Error("no recipient to seal to");
  }
  const { Encrypter } = await import("age-encryption");
  const encrypter = new Encrypter();
  for (const { recipient } of recipients) {
    encrypter.addRecipient(recipient);
  }
  const ct = await encrypter.encrypt(JSON.stringify(secrets));
  const fingerprints = await Promise.all(
    recipients.map(({ recipient }) => recipientFingerprint(recipient)),
  );
  return {
    keys: Object.keys(secrets).sort(),
    fingerprints: fingerprints.sort(),
    ct: toBase64(ct),
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
