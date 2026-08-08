import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Checkbox } from "@/components/ui/checkbox";
import { getFieldError } from "@/hooks/use-check-validation";
import type { CheckTypeModule } from "./index";
import type { CheckConfig, CheckTypeFieldsProps, FieldErrors } from "./common";
import { getConfigField } from "./common";

// ── ClickHouse (native protocol) ──
//
// Its own module rather than a member of `sqlDatabaseModule`: the native
// protocol carries a TLS toggle that moves the default port (9000 → 9440),
// which the shared SQL fields have no notion of.
export interface ClickhouseState {
  host: string;
  port: string;
  username: string;
  password: string;
  database: string;
  query: string;
  secure: boolean;
  tlsVerify: boolean;
}

export const clickhouseModule: CheckTypeModule<ClickhouseState> = {
  types: ["clickhouse"],
  fromConfig: (config) => ({
    host: getConfigField(config, "host"),
    port: getConfigField(config, "port"),
    username: getConfigField(config, "username"),
    password: getConfigField(config, "password"),
    database: getConfigField(config, "database"),
    query: getConfigField(config, "query"),
    secure: getConfigField(config, "secure") === "true",
    tlsVerify: getConfigField(config, "tls_verify") === "true",
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.username) cfg.username = state.username;
    if (state.password) cfg.password = state.password;
    if (state.database) cfg.database = state.database;
    if (state.query) cfg.query = state.query;
    if (state.secure) cfg.secure = true;
    // Certificate verification is meaningless without TLS, and the backend
    // rejects the pair — keep the sent config consistent with the checkbox
    // being disabled below.
    if (state.secure && state.tlsVerify) cfg.tls_verify = true;
    return {
      config: cfg,
      errors: state.host
        ? []
        : ([{ name: "host", message: "Host is required" }] as FieldErrors),
    };
  },
  Fields: ClickhouseFields,
};

function ClickhouseFields({
  state,
  onChange,
  errors,
}: CheckTypeFieldsProps<ClickhouseState>) {
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
        <div className="flex gap-2">
          <Input
            id="host"
            type="text"
            placeholder="clickhouse.example.com"
            value={state.host}
            onChange={(e) => onChange({ ...state, host: e.target.value })}
            className={cn(
              "flex-1",
              getFieldError(errors, "host") && "border-destructive",
            )}
            data-testid="check-host-input"
          />
          <Input
            id="port"
            type="number"
            placeholder={state.secure ? "9440" : "9000"}
            value={state.port}
            onChange={(e) => onChange({ ...state, port: e.target.value })}
            className="w-24"
            data-testid="check-port-input"
          />
        </div>
        {getFieldError(errors, "host") && (
          <p className="text-xs text-destructive">
            {getFieldError(errors, "host")}
          </p>
        )}
      </div>
      <div className="space-y-2">
        <Label htmlFor="username">Username (optional)</Label>
        <Input
          id="username"
          type="text"
          placeholder="default"
          value={state.username}
          onChange={(e) => onChange({ ...state, username: e.target.value })}
          data-testid="check-username-input"
        />
      </div>
      <div className="space-y-2">
        <Label htmlFor="password">Password (optional)</Label>
        <Input
          id="password"
          type="password"
          value={state.password}
          onChange={(e) => onChange({ ...state, password: e.target.value })}
          data-testid="check-password-input"
        />
      </div>
      <div className="space-y-2">
        <Label htmlFor="database">Database (optional)</Label>
        <Input
          id="database"
          type="text"
          placeholder="default"
          value={state.database}
          onChange={(e) => onChange({ ...state, database: e.target.value })}
          data-testid="check-database-input"
        />
      </div>
      <div className="space-y-2">
        <Label htmlFor="query">Query (optional)</Label>
        <Input
          id="query"
          type="text"
          placeholder="SELECT 1"
          value={state.query}
          onChange={(e) => onChange({ ...state, query: e.target.value })}
          data-testid="check-query-input"
        />
      </div>
      <div className="space-y-3">
        <label className="flex items-center gap-2">
          <Checkbox
            checked={state.secure}
            onCheckedChange={(v) =>
              onChange({
                ...state,
                secure: v === true,
                tlsVerify: v === true && state.tlsVerify,
              })
            }
            data-testid="check-clickhouse-secure-checkbox"
          />
          <span className="text-sm">Use TLS (native secure port)</span>
        </label>
        <label className="flex items-center gap-2">
          <Checkbox
            checked={state.tlsVerify}
            disabled={!state.secure}
            onCheckedChange={(v) => onChange({ ...state, tlsVerify: v === true })}
            data-testid="check-clickhouse-tls-verify-checkbox"
          />
          <span
            className={cn("text-sm", !state.secure && "text-muted-foreground")}
          >
            Verify TLS certificate
          </span>
        </label>
      </div>
    </>
  );
}
