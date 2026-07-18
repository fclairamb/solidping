import { useTranslation } from "react-i18next";
import { Plus, Trash2 } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { getFieldError } from "@/hooks/use-check-validation";
import type { CheckTypeModule } from "./index";
import type { CheckConfig, CheckTypeFieldsProps, FieldErrors } from "./common";
import { getConfigField } from "./common";
import { useCheckFormFields } from "./context";

export interface HttpState {
  url: string;
  method: string;
  expectedStatus: string;
  username: string;
  password: string;
  secretHeaders: { key: string; value: string }[];
  // Per-section dirty flags. Secrets are never returned by GET, so an untouched
  // section's inputs are empty and MUST NOT be serialized — omitting the keys is
  // what makes the server's preserve-absent-secrets merge keep the stored
  // values. Sending them anyway is what used to wipe secret headers on every
  // edit. Each flag is seeded by `fromConfig` from whether the config actually
  // carried those values, and set by the inputs' own onChange.
  authDirty: boolean;
  headersDirty: boolean;
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
  return {
    url: getConfigField(config, "url"),
    method: getConfigField(config, "method") || "GET",
    expectedStatus: getConfigField(config, "expectedStatus") || "200",
    username,
    password,
    secretHeaders,
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
  const statusCode = parseInt(state.expectedStatus, 10);
  if (!isNaN(statusCode) && statusCode !== 200) cfg.expectedStatus = statusCode;
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
  const errors: FieldErrors = state.url
    ? []
    : [{ name: "url", message: "URL is required" }];
  return { config: cfg, errors };
}

function Fields({ state, onChange, errors }: CheckTypeFieldsProps<HttpState>) {
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
        <Label htmlFor="expectedStatus">Expected Status</Label>
        <Input
          id="expectedStatus"
          type="number"
          placeholder="200"
          value={state.expectedStatus}
          onChange={(e) => onChange({ ...state, expectedStatus: e.target.value })}
          data-testid="check-expected-status-input"
        />
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
