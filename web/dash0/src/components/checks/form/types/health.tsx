// Application health check form (spec 2026-10-03-05). The request section is
// the http check's (method + URL, auth, headers, TLS, redirects); the health
// specific options are the document format, how old results may be, and which
// components to ignore or only warn about.
import { useTranslation } from "react-i18next";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
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
import { getConfigField, validationMessage } from "./common";
import { useCheckFormFields } from "./context";
import {
  HttpOptionsBody,
  HttpRequestLine,
  httpFromConfig,
  httpModule,
  httpToConfig,
} from "./http";
import type { HttpState } from "./http";

export const HEALTH_FORMATS = [
  "auto",
  "spatie",
  "spring",
  "ietf",
  "aspnet",
  "microprofile",
  "simple",
] as const;

export interface HealthState extends HttpState {
  format: string;
  // A Go duration ("10m"), "0" to disable, "" for the server default.
  maxAge: string;
  // One component name per line.
  ignore: string;
  // One component name per line: a failure of these only warns.
  warnOnly: string;
}

const MAX_AGE_PATTERN = /^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$|^0$/;

const lines = (text: string): string[] =>
  text
    .split("\n")
    .map((line) => line.trim())
    .filter((line) => line !== "");

const asList = (value: unknown): string[] =>
  Array.isArray(value) ? value.filter((v): v is string => typeof v === "string") : [];

/** The names whose `onFailed` is "warning" in a stored `components` map. */
export function warnOnlyNames(components: unknown): string[] {
  if (!components || typeof components !== "object" || Array.isArray(components)) return [];
  return Object.entries(components as Record<string, unknown>)
    .filter(([, value]) => {
      const entry = value as Record<string, unknown> | null;
      return (entry?.onFailed ?? entry?.on_failed) === "warning";
    })
    .map(([name]) => name);
}

/** Component names found in a health result output, in order. */
export function componentNamesOf(output: Record<string, unknown> | undefined): string[] {
  const components = output?.components;
  if (!Array.isArray(components)) return [];
  return components
    .map((c) => (c as Record<string, unknown> | null)?.name)
    .filter((name): name is string => typeof name === "string");
}

function fromConfig(config: CheckConfig): HealthState {
  const base = httpFromConfig(config);
  return {
    ...base,
    // The health document is the only judge of the response.
    jsonPathAssertions: null,
    bodyAssertions: null,
    expectedStatusCodes: ["200"],
    format: getConfigField(config, "format") || "auto",
    maxAge: getConfigField(config, "maxAge") || getConfigField(config, "max_age"),
    ignore: asList(config.ignore).join("\n"),
    warnOnly: warnOnlyNames(config.components).join("\n"),
  };
}

function toConfig(state: HealthState): { config: CheckConfig; errors: FieldErrors } {
  const { config, errors } = httpToConfig({
    ...state,
    jsonPathAssertions: null,
    bodyAssertions: null,
    expectedStatusCodes: ["200"],
  });
  const cfg: CheckConfig = { ...config };
  if (state.format && state.format !== "auto") cfg.format = state.format;
  const maxAge = state.maxAge.trim();
  if (maxAge) {
    cfg.maxAge = maxAge;
    if (!MAX_AGE_PATTERN.test(maxAge)) {
      errors.push({ name: "maxAge", message: validationMessage("healthMaxAge") });
    }
  }
  const ignore = lines(state.ignore);
  if (ignore.length > 0) cfg.ignore = ignore;
  const warn = lines(state.warnOnly);
  if (warn.length > 0) {
    cfg.components = Object.fromEntries(warn.map((name) => [name, { onFailed: "warning" }]));
  }
  return { config: cfg, errors };
}

/** Add a name to a one-per-line textarea value, once. */
export function addName(text: string, name: string): string {
  const current = lines(text);
  if (current.includes(name)) return text;
  return [...current, name].join("\n");
}

function Fields({ state, onChange, errors }: CheckTypeFieldsProps<HealthState>) {
  const { t } = useTranslation("checks");
  const { lastResultOutput } = useCheckFormFields();
  const known = componentNamesOf(lastResultOutput);
  const ignored = lines(state.ignore);
  const warned = lines(state.warnOnly);
  return (
    <>
      <HttpRequestLine state={state} onChange={onChange as unknown as (s: HttpState) => void} errors={errors} />
      <div className="space-y-2">
        <Label htmlFor="health-format">{t("health.format")}</Label>
        <Select value={state.format} onValueChange={(format) => onChange({ ...state, format })}>
          <SelectTrigger id="health-format" data-testid="check-health-format-select">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {HEALTH_FORMATS.map((format) => (
              <SelectItem key={format} value={format} data-testid={`check-health-format-${format}`}>
                {t(`health.formats.${format}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p className="text-xs text-muted-foreground">{t("health.formatHelp")}</p>
      </div>
      <div className="space-y-2">
        <Label htmlFor="health-max-age">{t("health.maxAge")}</Label>
        <Input
          id="health-max-age"
          placeholder="10m"
          value={state.maxAge}
          onChange={(e) => onChange({ ...state, maxAge: e.target.value })}
          data-testid="check-health-max-age-input"
        />
        <p className="text-xs text-muted-foreground">{t("health.maxAgeHelp")}</p>
        {getFieldError(errors, "maxAge") && (
          <p className="text-xs text-destructive">{getFieldError(errors, "maxAge")}</p>
        )}
      </div>
      <div className="space-y-2">
        <Label htmlFor="health-ignore">{t("health.ignore")}</Label>
        <Textarea
          id="health-ignore"
          rows={3}
          value={state.ignore}
          onChange={(e) => onChange({ ...state, ignore: e.target.value })}
          data-testid="check-health-ignore-input"
        />
        <p className="text-xs text-muted-foreground">{t("health.namesHelp")}</p>
      </div>
      <div className="space-y-2">
        <Label htmlFor="health-warn-only">{t("health.warnOnly")}</Label>
        <Textarea
          id="health-warn-only"
          rows={3}
          value={state.warnOnly}
          onChange={(e) => onChange({ ...state, warnOnly: e.target.value })}
          data-testid="check-health-warn-only-input"
        />
        <p className="text-xs text-muted-foreground">{t("health.warnOnlyHelp")}</p>
      </div>
      {known.length > 0 && (
        <div className="space-y-2" data-testid="check-health-suggestions">
          <Label>{t("health.knownComponents")}</Label>
          <div className="flex flex-wrap gap-2">
            {known.map((name) => (
              <div key={name} className="flex items-center gap-1 rounded-md border px-2 py-1 text-xs">
                <span className="font-mono">{name}</span>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-6 px-1"
                  disabled={ignored.includes(name)}
                  onClick={() => onChange({ ...state, ignore: addName(state.ignore, name) })}
                  data-testid={`check-health-ignore-${name}`}
                >
                  {t("health.suggestIgnore")}
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-6 px-1"
                  disabled={warned.includes(name)}
                  onClick={() => onChange({ ...state, warnOnly: addName(state.warnOnly, name) })}
                  data-testid={`check-health-warn-${name}`}
                >
                  {t("health.suggestWarn")}
                </Button>
              </div>
            ))}
          </div>
        </div>
      )}
    </>
  );
}

/** Advanced section body: the http request options minus the assertions. */
export function HealthAdvancedFields(props: CheckTypeFieldsProps<HealthState>) {
  return <HttpOptionsBody {...(props as unknown as CheckTypeFieldsProps<HttpState>)} assertions={false} />;
}

export function healthAdvancedSummary(state: HealthState): { text: string; customized: boolean } {
  const parts: string[] = [];
  if (state.httpVersion !== "1.1") parts.push(`HTTP/${state.httpVersion}`);
  if (!state.verifySsl) parts.push("TLS verification off");
  if (!state.followRedirects) parts.push("redirects not followed");
  if (state.captureFailureResponse) parts.push("failure response captured");
  if (state.body) parts.push("request body");
  const headerCount = state.headers.filter((h) => h.key).length;
  if (headerCount > 0) parts.push(`${headerCount} header${headerCount === 1 ? "" : "s"}`);
  return { text: parts.join(" · "), customized: parts.length > 0 };
}

export const healthModule: CheckTypeModule<HealthState> = {
  types: ["health"],
  // The http request keys (both spellings), then the health ones. The body and
  // status assertion keys are refused by the server on a health check, so they
  // are owned too: an old value can never ride along through the passthrough.
  ownedKeys: [
    ...httpModule.ownedKeys,
    "body_expect",
    "bodyExpect",
    "body_reject",
    "bodyReject",
    "body_pattern",
    "bodyPattern",
    "body_pattern_reject",
    "bodyPatternReject",
    "format",
    "maxAge",
    "max_age",
    "ignore",
    "components",
  ],
  fromConfig,
  toConfig,
  Fields,
};
