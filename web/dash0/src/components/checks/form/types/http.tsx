import { useTranslation } from "react-i18next";
import { Plus, Trash2 } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { TokenChipsInput } from "@/components/shared/token-chips-input";
import {
  dedupeStatusPatterns,
  isValidStatusPattern,
  normalizeStatusPattern,
} from "@/lib/http-status";
import { getFieldError } from "@/hooks/use-check-validation";
import type { CheckTypeModule } from "./index";
import type { CheckConfig, CheckTypeFieldsProps, FieldErrors } from "./common";
import { getConfigField } from "./common";
import { useCheckFormFields } from "./context";

export interface HttpState {
  url: string;
  method: string;
  // A list of exact codes ("200") and/or NXX wildcards ("4XX"). Always
  // normalized (trimmed + uppercased) and de-duplicated — see
  // dedupeStatusPatterns. Never empty in practice: fromConfig defaults it to
  // ["200"] when the config carries neither status key.
  expectedStatusCodes: string[];
  username: string;
  password: string;
  secretHeaders: { key: string; value: string }[];
  // Both default to true (today's behavior). false is the only value ever
  // written to config — see toConfig — matching the server's omit-at-default
  // GetConfig style.
  verifySsl: boolean;
  followRedirects: boolean;
  // Opt-in capture of what the probe received when the check FAILS, kept as
  // incident diagnostics. Defaults to false (off) — unlike the two above, whose
  // default is on — because a response body can contain PII or session
  // material. Only `true` is ever written to config, mirroring the server's
  // omit-at-default GetConfig.
  captureFailureResponse: boolean;
  // Per-section dirty flags. Secrets are never returned by GET, so an untouched
  // section's inputs are empty and MUST NOT be serialized — omitting the keys is
  // what makes the server's preserve-absent-secrets merge keep the stored
  // values. Sending them anyway is what used to wipe secret headers on every
  // edit. Each flag is seeded by `fromConfig` from whether the config actually
  // carried those values, and set by the inputs' own onChange.
  authDirty: boolean;
  headersDirty: boolean;
}

// seedExpectedStatusCodes implements the fromConfig precedence from spec
// 2026-07-21-02: prefer the new expectedStatusCodes list; else fall back to
// the legacy single expectedStatus int as one exact chip; else default to
// the implicit ["200"].
function seedExpectedStatusCodes(config: CheckConfig): string[] {
  const rawCodes = config.expectedStatusCodes;
  if (Array.isArray(rawCodes) && rawCodes.length > 0) {
    const deduped = dedupeStatusPatterns(rawCodes.map(String));
    if (deduped.length > 0) return deduped;
  }
  const legacy = config.expectedStatus;
  if (legacy !== undefined && legacy !== null && String(legacy) !== "") {
    const deduped = dedupeStatusPatterns([String(legacy)]);
    if (deduped.length > 0) return deduped;
  }
  return ["200"];
}

function fromConfig(config: CheckConfig): HttpState {
  const rawHeaders = config.secretHeaders;
  const hasHeaders =
    !!rawHeaders && typeof rawHeaders === "object" && !Array.isArray(rawHeaders);
  const secretHeaders = hasHeaders
    ? Object.entries(rawHeaders as Record<string, string>).map(
        ([key, value]) => ({ key, value }),
      )
    : [];
  const username = getConfigField(config, "username");
  const password = getConfigField(config, "password");
  // Both keys are only ever stored at their non-default (false) value — see
  // toConfig — so anything other than a literal `false` (absent, true, or a
  // malformed value) means "on", matching the server's default.
  const verifySsl = config.verifySsl !== false;
  const followRedirects = config.followRedirects !== false;
  // Canonical key is snake_case (the server accepts the camelCase alias on
  // read but always re-emits the snake one), so read both and prefer the
  // canonical spelling.
  const captureFailureResponse =
    config.capture_failure_response === true ||
    config.captureFailureResponse === true;
  return {
    url: getConfigField(config, "url"),
    method: getConfigField(config, "method") || "GET",
    expectedStatusCodes: seedExpectedStatusCodes(config),
    username,
    password,
    secretHeaders,
    verifySsl,
    followRedirects,
    captureFailureResponse,
    // Seeding matters three ways: a legacy row's username is public and comes
    // back, so it round-trips and folds into `basicAuth` on save; a prefill link
    // (`?username=probe`) must submit what it prefilled; and on a deployment
    // running the plaintext fallback the values do come back.
    authDirty: !!username || !!password,
    headersDirty: secretHeaders.length > 0,
  };
}

function toConfig(state: HttpState): { config: CheckConfig; errors: FieldErrors } {
  const cfg: CheckConfig = {};
  if (state.url) cfg.url = state.url;
  if (state.method && state.method !== "GET") cfg.method = state.method;
  // The default ["200"] (or an empty list) stays implicit — omit both status
  // keys, matching today's "200 is implicit" behavior. Otherwise write the
  // list and never the deprecated `expectedStatus`: toConfig rebuilds the
  // config object from state every time, so a legacy key from a previously
  // saved check drops off automatically the next time this form saves it.
  const codes = state.expectedStatusCodes;
  const isImplicitDefault =
    codes.length === 0 || (codes.length === 1 && codes[0] === "200");
  if (!isImplicitDefault) {
    cfg.expectedStatusCodes = codes;
  }
  if (state.authDirty) {
    if (state.username || state.password) {
      // The server folds this pair into the encrypted `basicAuth` key.
      if (state.username) cfg.username = state.username;
      if (state.password) cfg.password = state.password;
    } else {
      // Touched and emptied → explicitly clear the stored credential.
      cfg.basicAuth = null;
    }
  }
  if (state.headersDirty) {
    const shMap: Record<string, string> = {};
    for (const { key, value } of state.secretHeaders) {
      if (key) shMap[key] = value;
    }
    // An explicit {} clears; when the section is untouched the key is absent
    // entirely and the stored headers are preserved.
    cfg.secretHeaders = shMap;
  }
  // Only the non-default (false) value is ever written, matching the
  // server's GetConfig omit-at-default style — an unset/true config
  // round-trips without ever writing the key.
  if (!state.verifySsl) cfg.verifySsl = false;
  if (!state.followRedirects) cfg.followRedirects = false;
  // Written only when opted in, under the canonical snake_case key.
  if (state.captureFailureResponse) cfg.capture_failure_response = true;
  const errors: FieldErrors = [];
  if (!state.url) errors.push({ name: "url", message: "URL is required" });
  // Invalid chips block save with a field-scoped error, the same mechanism
  // the URL-required check above uses (see check-form.tsx's
  // `serialized.errors` / `blockingErrors`) — the chip itself is also flagged
  // destructive-red by TokenChipsInput, but that's a live hint, not what
  // gates the actual submit.
  const invalidCodes = codes.filter((code) => !isValidStatusPattern(code));
  if (invalidCodes.length > 0) {
    errors.push({
      name: "expectedStatusCodes",
      message: `Invalid status code pattern${invalidCodes.length > 1 ? "s" : ""}: ${invalidCodes.join(", ")} (use an exact code like 200 or a wildcard like 4XX)`,
    });
  }
  return { config: cfg, errors };
}

function Fields({ state, onChange, errors }: CheckTypeFieldsProps<HttpState>) {
  const { t } = useTranslation("checks");
  const invalidCodes = state.expectedStatusCodes.filter(
    (code) => !isValidStatusPattern(code),
  );
  const statusCodesError =
    invalidCodes.length > 0
      ? t("form.statusCodeInvalidSummary", "Invalid: {{codes}}", {
          codes: invalidCodes.join(", "),
        })
      : getFieldError(errors, "expected_status_codes");
  return (
    <>
      <div className="space-y-2">
        <Label>Request</Label>
        <div className="flex gap-2">
          <Select
            value={state.method}
            onValueChange={(method) => onChange({ ...state, method })}
          >
            <SelectTrigger className="w-28" data-testid="check-method-select">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"].map(
                (m) => (
                  <SelectItem key={m} value={m}>
                    {m}
                  </SelectItem>
                ),
              )}
            </SelectContent>
          </Select>
          <Input
            id="url"
            type="url"
            placeholder="https://example.com"
            value={state.url}
            onChange={(e) => onChange({ ...state, url: e.target.value })}
            className={cn(
              "flex-1",
              getFieldError(errors, "url") && "border-destructive",
            )}
            data-testid="check-url-input"
          />
        </div>
        {getFieldError(errors, "url") && (
          <p className="text-xs text-destructive">
            {getFieldError(errors, "url")}
          </p>
        )}
      </div>
      <div className="space-y-2">
        <Label htmlFor="expectedStatusCodes">Expected Status</Label>
        <TokenChipsInput
          id="expectedStatusCodes"
          value={state.expectedStatusCodes}
          onChange={(codes) => onChange({ ...state, expectedStatusCodes: codes })}
          validate={isValidStatusPattern}
          normalize={normalizeStatusPattern}
          placeholder="200"
          data-testid="check-expected-status-codes"
          invalidTitle={t(
            "form.statusCodeInvalid",
            "Not a valid status code or wildcard (e.g. 200, 4XX)",
          )}
          getRemoveLabel={(code) =>
            t("form.removeStatusCode", "Remove {{code}}", { code })
          }
        />
        <p className="text-xs text-muted-foreground">
          {t(
            "form.expectedStatusCodesHint",
            "Exact codes or ranges: 200, 201, 4XX",
          )}
        </p>
        {statusCodesError && (
          <p
            className="text-xs text-destructive"
            data-testid="check-expected-status-codes-error"
          >
            {statusCodesError}
          </p>
        )}
      </div>
    </>
  );
}

// AuthFields renders the "Authentication & secrets" section body: Basic-Auth
// credentials plus editable secret request headers.
export function HttpAuthFields({
  state,
  onChange,
}: CheckTypeFieldsProps<HttpState>) {
  const { t } = useTranslation("checks");
  const { configPrivateKeys } = useCheckFormFields();
  const { secretHeaders } = state;
  return (
    <>
      <div className="space-y-2">
        <div className="flex gap-4">
          <div className="space-y-2 flex-1">
            <Label htmlFor="username">Username (optional, Basic Auth)</Label>
            <Input
              id="username"
              type="text"
              placeholder="user"
              value={state.username}
              onChange={(e) =>
                onChange({ ...state, username: e.target.value, authDirty: true })
              }
              data-testid="check-username-input"
            />
          </div>
          <div className="space-y-2 flex-1">
            <Label htmlFor="password">Password (optional)</Label>
            <Input
              id="password"
              type="password"
              value={state.password}
              onChange={(e) =>
                onChange({ ...state, password: e.target.value, authDirty: true })
              }
              data-testid="check-password-input"
            />
          </div>
        </div>
        {configPrivateKeys?.includes("basicAuth") && !state.authDirty && (
          <p className="text-xs text-muted-foreground" data-testid="basic-auth-encrypted">
            <span className="font-mono tracking-widest">••••</span>{" "}
            <span className="italic">
              (encrypted — enter new values to replace)
            </span>
          </p>
        )}
      </div>
      <div className="space-y-2">
        <div>
          <Label>{t("secretHeaders")}</Label>
          <p className="text-xs text-muted-foreground mt-0.5">
            {t("secretHeadersDescription")}
          </p>
        </div>
        {configPrivateKeys?.includes("secretHeaders") &&
          !state.headersDirty && (
            <p className="text-xs text-muted-foreground">
              <span className="font-mono tracking-widest">••••</span>{" "}
              <span className="italic">
                (encrypted — enter new values to replace)
              </span>
            </p>
          )}
        {secretHeaders.map((row, idx) => (
          <div key={idx} className="flex gap-2 items-center">
            <Input
              type="text"
              placeholder="Header-Name"
              value={row.key}
              onChange={(e) => {
                const updated = [...secretHeaders];
                updated[idx] = { ...updated[idx], key: e.target.value };
                onChange({ ...state, secretHeaders: updated, headersDirty: true });
              }}
              className="flex-1"
              data-testid={`secret-header-key-${idx}`}
            />
            <Input
              type="password"
              placeholder="value"
              value={row.value}
              onChange={(e) => {
                const updated = [...secretHeaders];
                updated[idx] = { ...updated[idx], value: e.target.value };
                onChange({ ...state, secretHeaders: updated, headersDirty: true });
              }}
              className="flex-1"
              data-testid={`secret-header-value-${idx}`}
            />
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="text-destructive shrink-0"
              onClick={() =>
                onChange({
                  ...state,
                  secretHeaders: secretHeaders.filter((_, i) => i !== idx),
                  headersDirty: true,
                })
              }
              data-testid={`secret-header-remove-${idx}`}
            >
              <Trash2 className="h-4 w-4" />
            </Button>
          </div>
        ))}
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() =>
            // Adding a blank row is deliberately NOT dirtying: typing into it
            // is. Otherwise a stray click on "add" followed by a save would
            // clear the stored headers.
            onChange({
              ...state,
              secretHeaders: [...secretHeaders, { key: "", value: "" }],
            })
          }
          data-testid="add-secret-header-button"
        >
          <Plus className="h-4 w-4 mr-1" />
          {t("addSecretHeader")}
        </Button>
      </div>
    </>
  );
}

// OptionsFields renders the "Advanced" section's HTTP-specific toggles:
// TLS certificate verification and redirect following. Both default on
// (today's hardcoded behavior).
export function HttpOptionsFields({
  state,
  onChange,
}: CheckTypeFieldsProps<HttpState>) {
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2">
        <Switch
          id="http-verify-ssl"
          checked={state.verifySsl}
          onCheckedChange={(verifySsl) => onChange({ ...state, verifySsl })}
          data-testid="check-verify-ssl-switch"
        />
        <Label htmlFor="http-verify-ssl">Verify TLS certificate</Label>
      </div>
      {!state.verifySsl && (
        <p
          className="text-xs text-yellow-700 dark:text-yellow-400"
          data-testid="check-verify-ssl-warning"
        >
          Certificate errors will be ignored — the check will report up even
          for an invalid, expired, or self-signed certificate.
        </p>
      )}
      <div className="flex items-center gap-2">
        <Switch
          id="http-follow-redirects"
          checked={state.followRedirects}
          onCheckedChange={(followRedirects) =>
            onChange({ ...state, followRedirects })
          }
          data-testid="check-follow-redirects-switch"
        />
        <Label htmlFor="http-follow-redirects">Follow redirects</Label>
      </div>
      {!state.followRedirects && (
        <p className="text-xs text-muted-foreground">
          The check will evaluate the first response (e.g. a 3XX) instead of
          following it to its destination.
        </p>
      )}
      <div className="flex items-center gap-2">
        <Switch
          id="http-capture-failure-response"
          checked={state.captureFailureResponse}
          onCheckedChange={(captureFailureResponse) =>
            onChange({ ...state, captureFailureResponse })
          }
          data-testid="check-capture-failure-response-switch"
        />
        <Label htmlFor="http-capture-failure-response">
          Capture the failing response
        </Label>
      </div>
      {state.captureFailureResponse && (
        <p
          className="text-xs text-yellow-700 dark:text-yellow-400"
          data-testid="check-capture-failure-response-warning"
        >
          When this check fails, the response it received (status line, headers
          with sensitive values redacted, and up to 16 KB of the body) is kept
          on the incident it opens. Response bodies can contain personal data —
          the capture is visible to your team only and never appears on a status
          page.
        </p>
      )}
    </div>
  );
}

// httpOptionsSummary drives the "Advanced" section's summary line/customized
// flag for the HTTP-specific toggles above.
export function httpOptionsSummary(state: HttpState): {
  text: string;
  customized: boolean;
} {
  const parts: string[] = [];
  if (!state.verifySsl) parts.push("TLS verification off");
  if (!state.followRedirects) parts.push("redirects not followed");
  if (state.captureFailureResponse) parts.push("failure response captured");
  return { text: parts.join(" · "), customized: parts.length > 0 };
}

export const httpModule: CheckTypeModule<HttpState> = {
  types: ["http"],
  fromConfig,
  toConfig,
  Fields,
};

// "Authentication & secrets" summary for the collapsed section header.
//
// `configPrivateKeys` is load-bearing: a folded check's credential and secret
// headers are encrypted server-side and never come back on GET, so the state
// alone would render "none" for a check that very much has credentials.
export function httpAuthSummary(
  state: HttpState,
  configPrivateKeys?: string[],
): {
  text: string;
  customized: boolean;
} {
  const storedAuth = !!configPrivateKeys?.includes("basicAuth");
  const storedHeaders = !!configPrivateKeys?.includes("secretHeaders");
  const parts: string[] = [];
  const hasAuth = state.authDirty
    ? !!state.username || !!state.password
    : !!state.username || storedAuth;
  if (hasAuth) parts.push("basic auth");
  const headerCount = state.secretHeaders.filter((h) => h.key).length;
  if (headerCount > 0)
    parts.push(`${headerCount} secret header${headerCount === 1 ? "" : "s"}`);
  else if (!state.headersDirty && storedHeaders) parts.push("secret headers");
  const customized = parts.length > 0;
  return { text: parts.join(" · ") || "none", customized };
}
