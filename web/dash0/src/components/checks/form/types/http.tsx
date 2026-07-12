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
}

function fromConfig(config: CheckConfig): HttpState {
  const rawHeaders = config.secretHeaders;
  const secretHeaders =
    rawHeaders && typeof rawHeaders === "object" && !Array.isArray(rawHeaders)
      ? Object.entries(rawHeaders as Record<string, string>).map(
          ([key, value]) => ({ key, value }),
        )
      : [];
  return {
    url: getConfigField(config, "url"),
    method: getConfigField(config, "method") || "GET",
    expectedStatus: getConfigField(config, "expectedStatus") || "200",
    username: getConfigField(config, "username"),
    password: getConfigField(config, "password"),
    secretHeaders,
  };
}

function toConfig(state: HttpState): { config: CheckConfig; errors: FieldErrors } {
  const cfg: CheckConfig = {};
  if (state.url) cfg.url = state.url;
  if (state.method && state.method !== "GET") cfg.method = state.method;
  const statusCode = parseInt(state.expectedStatus, 10);
  if (!isNaN(statusCode) && statusCode !== 200) cfg.expectedStatus = statusCode;
  if (state.username) cfg.username = state.username;
  if (state.password) cfg.password = state.password;
  const shMap: Record<string, string> = {};
  for (const { key, value } of state.secretHeaders) {
    if (key) shMap[key] = value;
  }
  if (Object.keys(shMap).length > 0) cfg.secretHeaders = shMap;
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
      <div className="flex gap-4">
        <div className="space-y-2 flex-1">
          <Label htmlFor="username">Username (optional, Basic Auth)</Label>
          <Input
            id="username"
            type="text"
            placeholder="user"
            value={state.username}
            onChange={(e) => onChange({ ...state, username: e.target.value })}
            data-testid="check-username-input"
          />
        </div>
        <div className="space-y-2 flex-1">
          <Label htmlFor="password">Password (optional)</Label>
          <Input
            id="password"
            type="password"
            value={state.password}
            onChange={(e) => onChange({ ...state, password: e.target.value })}
            data-testid="check-password-input"
          />
        </div>
      </div>
      <div className="space-y-2">
        <div>
          <Label>{t("secretHeaders")}</Label>
          <p className="text-xs text-muted-foreground mt-0.5">
            {t("secretHeadersDescription")}
          </p>
        </div>
        {configPrivateKeys?.includes("secretHeaders") &&
          secretHeaders.length === 0 && (
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
                onChange({ ...state, secretHeaders: updated });
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
                onChange({ ...state, secretHeaders: updated });
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
export function httpAuthSummary(state: HttpState): {
  text: string;
  customized: boolean;
} {
  const parts: string[] = [];
  if (state.username) parts.push("basic auth");
  const headerCount = state.secretHeaders.filter((h) => h.key).length;
  if (headerCount > 0)
    parts.push(`${headerCount} secret header${headerCount === 1 ? "" : "s"}`);
  const customized =
    !!state.username || !!state.password || headerCount > 0;
  return { text: parts.join(" · ") || "none", customized };
}
