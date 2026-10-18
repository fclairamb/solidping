// Fallback module for check types the dashboard has no form for: plugin types
// loaded by the server (spec 2026-10-17-05). The config is edited as JSON with
// CodeMirror and checked against the type's JSON Schema, which the server
// serves at /api/v1/orgs/:org/check-types/:type/schema (agent-reported types
// included, spec 2026-10-17-05 §18) and also validates with on save.
import { useEffect } from "react";
import CodeMirror from "@uiw/react-codemirror";
import { json } from "@codemirror/lang-json";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { Label } from "@/components/ui/label";
import { useCheckConfigSchema, type CheckConfigSchema } from "@/api/hooks";
import { lintAgainstSchema } from "@/lib/json-schema-lint";
import type { CheckTypeModule } from "./index";
import { useCheckFormFields } from "./context";
import {
  SHARED_FORM_CONFIG_KEYS,
  validationMessage,
  type CheckConfig,
  type CheckTypeFieldsProps,
  type FieldErrors,
} from "./common";

export interface PluginState {
  type: string;
  text: string;
  schema?: CheckConfigSchema;
}

/** The wildcard ownedKeys entry: this module models every config key. */
export const OWNS_ALL_KEYS = "*";

function configToText(config: CheckConfig): string {
  const own: CheckConfig = {};
  for (const [key, value] of Object.entries(config)) {
    if (!SHARED_FORM_CONFIG_KEYS.includes(key)) own[key] = value;
  }
  return JSON.stringify(own, null, 2);
}

// parseConfig returns the config object, or the reason the text is not one.
export function parseConfig(text: string): { config?: CheckConfig; error?: string } {
  let value: unknown;
  try {
    value = JSON.parse(text.trim() === "" ? "{}" : text);
  } catch (err) {
    return { error: validationMessage("pluginInvalidJson", { detail: (err as Error).message }) };
  }
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    return { error: validationMessage("pluginNotAnObject") };
  }
  return { config: value as CheckConfig };
}

export function pluginErrors(state: PluginState): { config: CheckConfig; errors: FieldErrors } {
  const parsed = parseConfig(state.text);
  if (!parsed.config) {
    return { config: {}, errors: [{ name: "config", message: parsed.error ?? "" }] };
  }
  const errors = lintAgainstSchema(parsed.config, state.schema).map((issue) => ({
    name: issue.path || "config",
    message: `${issue.path || "config"}: ${issue.message}`,
  }));
  return { config: parsed.config, errors };
}

/** Builds the fallback module for one plugin type. */
export function pluginModule(type: string): CheckTypeModule<PluginState> {
  return {
    types: [type],
    ownedKeys: [OWNS_ALL_KEYS],
    fromConfig: (config) => ({ type, text: configToText(config) }),
    toConfig: (state) => pluginErrors(state),
    Fields: PluginFields,
  };
}

function PluginFields({ state, onChange, errors }: CheckTypeFieldsProps<PluginState>) {
  const { t } = useTranslation("checks");
  const { org } = useCheckFormFields();
  const { data: schema } = useCheckConfigSchema(org, state.type);

  // Keep the schema in the state so toConfig (pure) can lint against it.
  useEffect(() => {
    if (schema && state.schema !== schema) onChange({ ...state, schema });
  }, [schema, state, onChange]);

  const description = typeof schema?.description === "string" ? schema.description : "";

  // The editor is linted as it is typed (JSON syntax, then the schema); the
  // server's own findings for the config come in through `errors`.
  const local = pluginErrors({ ...state, schema: state.schema ?? schema }).errors;
  const shown = [
    ...local,
    ...errors.filter((e) => !local.some((l) => l.name === e.name)),
  ];

  return (
    <div className="space-y-2" data-testid="plugin-config">
      <Label htmlFor="plugin-config-editor">{t("plugin.configLabel")}</Label>
      <CodeMirror
        id="plugin-config-editor"
        value={state.text}
        onChange={(text) => onChange({ ...state, text })}
        extensions={[json()]}
        theme={document.documentElement.classList.contains("dark") ? "dark" : "light"}
        height="220px"
        className={cn("rounded-md border text-sm", shown.length > 0 && "border-destructive")}
        data-testid="plugin-config-editor"
      />
      {shown.length > 0 && (
        <ul className="space-y-1 text-xs text-destructive" data-testid="plugin-config-errors">
          {shown.map((error) => (
            <li key={`${error.name}-${error.message}`}>{error.message}</li>
          ))}
        </ul>
      )}
      {description && <p className="text-xs text-muted-foreground">{description}</p>}
      <p className="text-xs text-muted-foreground">{t("plugin.configHelp")}</p>
    </div>
  );
}
