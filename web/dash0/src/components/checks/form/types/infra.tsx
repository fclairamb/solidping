import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "@tanstack/react-router";
import { cn } from "@/lib/utils";
import { Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { CollapsibleSection } from "@/components/ui/collapsible-section";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { getFieldError } from "@/hooks/use-check-validation";
import { canSource } from "@/api/hooks";
import type { CheckTypeModule } from "./index";
import type { CheckConfig, CheckTypeFieldsProps, FieldErrors } from "./common";
import { durationStringToSeconds, getConfigField } from "./common";
import { useCheckFormFields } from "./context";

// ── SNMP ──
export interface SnmpState {
  host: string;
  port: string;
  oid: string;
  community: string;
  expectedValue: string;
  operator: string;
}

export const snmpModule: CheckTypeModule<SnmpState> = {
  types: ["snmp"],
  fromConfig: (config) => ({
    host: getConfigField(config, "host"),
    port: getConfigField(config, "port"),
    oid: getConfigField(config, "oid"),
    community: getConfigField(config, "community"),
    expectedValue: getConfigField(config, "expectedValue"),
    operator: getConfigField(config, "operator") || "equals",
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.oid) cfg.oid = state.oid;
    if (state.community) cfg.community = state.community;
    if (state.expectedValue) cfg.expectedValue = state.expectedValue;
    if (state.operator && state.operator !== "equals")
      cfg.operator = state.operator;
    const errors: FieldErrors = state.host
      ? []
      : [{ name: "host", message: "Host is required" }];
    return { config: cfg, errors };
  },
  Fields: SnmpFields,
};

function SnmpFields({ state, onChange, errors }: CheckTypeFieldsProps<SnmpState>) {
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
        <div className="flex gap-2">
          <Input
            id="host"
            type="text"
            placeholder="192.168.1.1"
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
            placeholder="161"
            value={state.port}
            onChange={(e) => onChange({ ...state, port: e.target.value })}
            className={cn(
              "w-24",
              getFieldError(errors, "port") && "border-destructive",
            )}
            data-testid="check-port-input"
          />
        </div>
        {getFieldError(errors, "host") && (
          <p className="text-xs text-destructive">
            {getFieldError(errors, "host")}
          </p>
        )}
        {getFieldError(errors, "port") && (
          <p className="text-xs text-destructive">
            {getFieldError(errors, "port")}
          </p>
        )}
      </div>
      <div className="space-y-2">
        <Label htmlFor="oid">OID</Label>
        <Input
          id="oid"
          type="text"
          placeholder=".1.3.6.1.2.1.1.1.0"
          value={state.oid}
          onChange={(e) => onChange({ ...state, oid: e.target.value })}
          className={cn(getFieldError(errors, "oid") && "border-destructive")}
          data-testid="check-oid-input"
        />
        {getFieldError(errors, "oid") && (
          <p className="text-xs text-destructive">
            {getFieldError(errors, "oid")}
          </p>
        )}
      </div>
      <div className="space-y-2">
        <Label htmlFor="community">Community (optional, default: public)</Label>
        <Input
          id="community"
          type="text"
          placeholder="public"
          value={state.community}
          onChange={(e) => onChange({ ...state, community: e.target.value })}
          data-testid="check-community-input"
        />
      </div>
      <div className="flex gap-4">
        <div className="space-y-2 flex-1">
          <Label htmlFor="expectedValue">Expected Value (optional)</Label>
          <Input
            id="expectedValue"
            type="text"
            placeholder=""
            value={state.expectedValue}
            onChange={(e) =>
              onChange({ ...state, expectedValue: e.target.value })
            }
            data-testid="check-expected-value-input"
          />
        </div>
        <div className="space-y-2 w-40">
          <Label htmlFor="snmpOperator">Operator</Label>
          <Select
            value={state.operator}
            onValueChange={(operator) => onChange({ ...state, operator })}
          >
            <SelectTrigger data-testid="check-operator-select">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="equals">Equals</SelectItem>
              <SelectItem value="not_equals">Not Equals</SelectItem>
              <SelectItem value="contains">Contains</SelectItem>
              <SelectItem value="greater_than">Greater Than</SelectItem>
              <SelectItem value="less_than">Less Than</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>
    </>
  );
}

// ── Docker ──
export interface DockerState {
  containerName: string;
  containerId: string;
  host: string;
  restartLoopMinRestarts: string;
  restartLoopWindowSeconds: string;
}

export const dockerModule: CheckTypeModule<DockerState> = {
  types: ["docker"],
  fromConfig: (config) => ({
    containerName: getConfigField(config, "containerName"),
    containerId: getConfigField(config, "containerId"),
    host: getConfigField(config, "host"),
    restartLoopMinRestarts: getConfigField(config, "restartLoopMinRestarts"),
    restartLoopWindowSeconds: durationStringToSeconds(
      getConfigField(config, "restartLoopWindow"),
    ),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.containerName) cfg.containerName = state.containerName;
    if (state.containerId) cfg.containerId = state.containerId;
    if (state.host) cfg.host = state.host;
    if (state.restartLoopMinRestarts)
      cfg.restartLoopMinRestarts = parseInt(state.restartLoopMinRestarts, 10);
    if (state.restartLoopWindowSeconds)
      cfg.restartLoopWindow = `${parseInt(state.restartLoopWindowSeconds, 10)}s`;
    const errors: FieldErrors =
      state.containerName || state.containerId
        ? []
        : [
            {
              name: "containerName",
              message: "Container name or ID is required",
            },
          ];
    return { config: cfg, errors };
  },
  Fields: DockerFields,
};

function DockerFields({
  state,
  onChange,
  errors,
}: CheckTypeFieldsProps<DockerState>) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="containerName">Container Name</Label>
        <Input
          id="containerName"
          type="text"
          placeholder="postgres"
          value={state.containerName}
          onChange={(e) => onChange({ ...state, containerName: e.target.value })}
          className={cn(
            getFieldError(errors, "containerName") && "border-destructive",
          )}
          data-testid="check-container-name-input"
        />
        {getFieldError(errors, "containerName") && (
          <p className="text-xs text-destructive">
            {getFieldError(errors, "containerName")}
          </p>
        )}
      </div>
      <div className="space-y-2">
        <Label htmlFor="containerId">
          Container ID (optional, alternative to name)
        </Label>
        <Input
          id="containerId"
          type="text"
          placeholder="abc123def456"
          value={state.containerId}
          onChange={(e) => onChange({ ...state, containerId: e.target.value })}
          data-testid="check-container-id-input"
        />
      </div>
      <div className="space-y-2">
        <Label htmlFor="host">Docker Host (optional)</Label>
        <Input
          id="host"
          type="text"
          placeholder="unix:///var/run/docker.sock"
          value={state.host}
          onChange={(e) => onChange({ ...state, host: e.target.value })}
          data-testid="check-host-input"
        />
        <p className="text-xs text-muted-foreground">
          Default: unix:///var/run/docker.sock. Use tcp://host:port for remote
          Docker daemons.
        </p>
      </div>
      <div className="space-y-2">
        <button
          type="button"
          className="text-sm underline"
          onClick={() => setOpen((v) => !v)}
        >
          {open ? "▼ " : "▶ "}Restart-loop detection (advanced)
        </button>
        {open && (
          <div className="space-y-2 pl-4 border-l">
            <p className="text-xs text-muted-foreground">
              Flag a running container as crash-looping when it has restarted at
              least N times and (re)started within the recency window. Leave Min
              Restarts empty (or 0) to disable. A detected loop reports a Warning
              (amber) — it counts as up and does not page.
            </p>
            <div className="space-y-1">
              <Label htmlFor="restartLoopMinRestarts">
                Min Restarts (0 = disabled)
              </Label>
              <Input
                id="restartLoopMinRestarts"
                type="number"
                min="0"
                placeholder="3"
                value={state.restartLoopMinRestarts}
                onChange={(e) =>
                  onChange({
                    ...state,
                    restartLoopMinRestarts: e.target.value,
                  })
                }
                data-testid="check-restart-loop-min-input"
              />
            </div>
            <div className="space-y-1">
              <Label htmlFor="restartLoopWindowSeconds">
                Window (seconds, default 120)
              </Label>
              <Input
                id="restartLoopWindowSeconds"
                type="number"
                min="1"
                placeholder="120"
                value={state.restartLoopWindowSeconds}
                onChange={(e) =>
                  onChange({
                    ...state,
                    restartLoopWindowSeconds: e.target.value,
                  })
                }
                data-testid="check-restart-loop-window-input"
              />
            </div>
          </div>
        )}
      </div>
    </>
  );
}

// ── Freebox line ──
export interface FreeboxLineState {
  connectionUid: string;
  linkType: string;
  minSyncRate: string;
  minSnrDb: string;
  maxAttnDb: string;
  maxCrcErrors: string;
  minRxMw: string;
  maxRxMw: string;
}

export const freeboxLineModule: CheckTypeModule<FreeboxLineState> = {
  types: ["freebox_line"],
  fromConfig: (config) => ({
    connectionUid: getConfigField(config, "connectionUid"),
    linkType: getConfigField(config, "linkType") || "xdsl",
    minSyncRate: getConfigField(config, "minSyncRateDownKbps"),
    minSnrDb: getConfigField(config, "minSnrMarginDownDb"),
    maxAttnDb: getConfigField(config, "maxAttenuationDb"),
    maxCrcErrors: getConfigField(config, "maxCrcErrorsPerRun"),
    minRxMw: getConfigField(config, "minRxPowerMw"),
    maxRxMw: getConfigField(config, "maxRxPowerMw"),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.connectionUid) cfg.connectionUid = state.connectionUid;
    if (state.linkType) cfg.linkType = state.linkType;
    if (state.minSyncRate)
      cfg.minSyncRateDownKbps = parseInt(state.minSyncRate, 10);
    if (state.minSnrDb) cfg.minSnrMarginDownDb = parseInt(state.minSnrDb, 10);
    if (state.maxAttnDb) cfg.maxAttenuationDb = parseInt(state.maxAttnDb, 10);
    if (state.maxCrcErrors)
      cfg.maxCrcErrorsPerRun = parseInt(state.maxCrcErrors, 10);
    if (state.minRxMw) cfg.minRxPowerMw = parseFloat(state.minRxMw);
    if (state.maxRxMw) cfg.maxRxPowerMw = parseFloat(state.maxRxMw);
    const errors: FieldErrors = [];
    if (!state.connectionUid)
      errors.push({
        name: "connectionUid",
        message: "Freebox connection is required",
      });
    else if (state.linkType !== "xdsl" && state.linkType !== "ftth")
      errors.push({
        name: "linkType",
        message: "Link type must be xdsl or ftth",
      });
    return { config: cfg, errors };
  },
  Fields: FreeboxLineFields,
};

function FreeboxLineFields({
  state,
  onChange,
}: CheckTypeFieldsProps<FreeboxLineState>) {
  const { t } = useTranslation("checks");
  const { org, connections } = useCheckFormFields();
  const [open, setOpen] = useState(false);
  // Filter by the canSource capability rather than a hard-coded type so future
  // data-source integrations are picked up automatically (freebox only today).
  const freeboxConnections = (connections ?? []).filter((c) =>
    canSource(c.type),
  );
  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="freeboxConnectionUid">
          {t("freeboxLine.connectionUid")}
        </Label>
        {freeboxConnections.length === 0 ? (
          <Alert>
            <AlertDescription>
              {t("freeboxLine.noConnections")}{" "}
              <Link
                to="/orgs/$org/integrations"
                params={{ org }}
                className="underline"
              >
                Integrations
              </Link>
            </AlertDescription>
          </Alert>
        ) : (
          <Select
            value={state.connectionUid}
            onValueChange={(connectionUid) =>
              onChange({ ...state, connectionUid })
            }
          >
            <SelectTrigger
              id="freeboxConnectionUid"
              data-testid="check-freebox-connection-select"
            >
              <SelectValue placeholder={t("freeboxLine.connectionUid")} />
            </SelectTrigger>
            <SelectContent>
              {freeboxConnections.map((c) => (
                <SelectItem key={c.uid} value={c.uid}>
                  {c.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
        <p className="text-xs text-muted-foreground">
          {t("freeboxLine.connectionUidHelp")}
        </p>
      </div>
      <div className="space-y-2">
        <Label htmlFor="freeboxLinkType">{t("freeboxLine.linkType")}</Label>
        <Select
          value={state.linkType}
          onValueChange={(linkType) => onChange({ ...state, linkType })}
        >
          <SelectTrigger
            id="freeboxLinkType"
            data-testid="check-freebox-linktype-select"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="xdsl">{t("freeboxLine.linkTypeXdsl")}</SelectItem>
            <SelectItem value="ftth">{t("freeboxLine.linkTypeFtth")}</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div className="space-y-2">
        <button
          type="button"
          className="text-sm underline"
          onClick={() => setOpen((v) => !v)}
        >
          {open ? "▼ " : "▶ "}
          {t("freeboxLine.advancedThresholds")}
        </button>
        {open && (
          <div className="space-y-2 pl-4 border-l">
            <p className="text-xs text-muted-foreground">
              {t("freeboxLine.thresholdsHelp")}
            </p>
            {state.linkType === "xdsl" && (
              <>
                <div className="space-y-1">
                  <Label htmlFor="freeboxMinSyncRate">
                    {t("freeboxLine.minSyncRateDownKbps")}
                  </Label>
                  <Input
                    id="freeboxMinSyncRate"
                    type="number"
                    min="0"
                    placeholder="0"
                    value={state.minSyncRate}
                    onChange={(e) =>
                      onChange({ ...state, minSyncRate: e.target.value })
                    }
                    data-testid="check-freebox-minsync-input"
                  />
                </div>
                <div className="space-y-1">
                  <Label htmlFor="freeboxMinSnr">
                    {t("freeboxLine.minSnrMarginDownDb")}
                  </Label>
                  <Input
                    id="freeboxMinSnr"
                    type="number"
                    min="0"
                    placeholder="0"
                    value={state.minSnrDb}
                    onChange={(e) =>
                      onChange({ ...state, minSnrDb: e.target.value })
                    }
                    data-testid="check-freebox-minsnr-input"
                  />
                </div>
                <div className="space-y-1">
                  <Label htmlFor="freeboxMaxAttn">
                    {t("freeboxLine.maxAttenuationDb")}
                  </Label>
                  <Input
                    id="freeboxMaxAttn"
                    type="number"
                    min="0"
                    placeholder="0"
                    value={state.maxAttnDb}
                    onChange={(e) =>
                      onChange({ ...state, maxAttnDb: e.target.value })
                    }
                    data-testid="check-freebox-maxattn-input"
                  />
                </div>
                <div className="space-y-1">
                  <Label htmlFor="freeboxMaxCrc">
                    {t("freeboxLine.maxCrcErrorsPerRun")}
                  </Label>
                  <Input
                    id="freeboxMaxCrc"
                    type="number"
                    min="0"
                    placeholder="0"
                    value={state.maxCrcErrors}
                    onChange={(e) =>
                      onChange({ ...state, maxCrcErrors: e.target.value })
                    }
                    data-testid="check-freebox-maxcrc-input"
                  />
                </div>
              </>
            )}
            {state.linkType === "ftth" && (
              <>
                <div className="space-y-1">
                  <Label htmlFor="freeboxMinRxMw">
                    {t("freeboxLine.minRxPowerMw")}
                  </Label>
                  <Input
                    id="freeboxMinRxMw"
                    type="number"
                    min="0"
                    step="0.001"
                    placeholder="0"
                    value={state.minRxMw}
                    onChange={(e) =>
                      onChange({ ...state, minRxMw: e.target.value })
                    }
                    data-testid="check-freebox-minrx-input"
                  />
                </div>
                <div className="space-y-1">
                  <Label htmlFor="freeboxMaxRxMw">
                    {t("freeboxLine.maxRxPowerMw")}
                  </Label>
                  <Input
                    id="freeboxMaxRxMw"
                    type="number"
                    min="0"
                    step="0.001"
                    placeholder="0"
                    value={state.maxRxMw}
                    onChange={(e) =>
                      onChange({ ...state, maxRxMw: e.target.value })
                    }
                    data-testid="check-freebox-maxrx-input"
                  />
                </div>
              </>
            )}
          </div>
        )}
      </div>
    </>
  );
}

// ── Prometheus ──
//
// The first check type that grades a *value* rather than a service, so the form
// is shaped around the value: pick where it comes from (mode), which number to
// read (metric+labels, or a PromQL query), then how to grade it (operator +
// warning/critical). Everything that has a sane default — match, onMissing,
// headers — lives behind the Advanced section.
export interface PrometheusLabelRow {
  key: string;
  value: string;
}

export interface PrometheusState {
  url: string;
  mode: string;
  metric: string;
  labels: PrometheusLabelRow[];
  query: string;
  operator: string;
  warningValue: string;
  criticalValue: string;
  match: string;
  onMissing: string;
  headers: PrometheusLabelRow[];
}

// rowsFromRecord turns a config object into the editable row list the form
// uses. An empty object yields no rows (not one blank row) so `toConfig` can
// tell "nothing configured" from "a row the user is still typing into".
function rowsFromRecord(value: unknown): PrometheusLabelRow[] {
  if (!value || typeof value !== "object" || Array.isArray(value)) return [];
  return Object.entries(value as Record<string, unknown>).map(([key, v]) => ({
    key,
    value: String(v ?? ""),
  }));
}

function recordFromRows(rows: PrometheusLabelRow[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const row of rows) {
    if (row.key.trim()) out[row.key.trim()] = row.value;
  }
  return out;
}

// numberFromConfig keeps an explicit 0 visible. Thresholds are floats and 0 is
// a legal threshold ("alert when free slots reach 0"), so the usual
// falsy-means-absent shortcut would silently drop it.
function numberFromConfig(config: CheckConfig, field: string): string {
  const value = config?.[field];
  if (value === undefined || value === null || value === "") return "";
  return String(value);
}

export const prometheusModule: CheckTypeModule<PrometheusState> = {
  types: ["prometheus"],
  fromConfig: (config) => ({
    url: getConfigField(config, "url"),
    mode: getConfigField(config, "mode") || "scrape",
    metric: getConfigField(config, "metric"),
    labels: rowsFromRecord(config?.labels),
    query: getConfigField(config, "query"),
    operator: getConfigField(config, "operator") || ">",
    warningValue: numberFromConfig(config, "warningValue"),
    criticalValue: numberFromConfig(config, "criticalValue"),
    match: getConfigField(config, "match") || "single",
    onMissing: getConfigField(config, "onMissing") || "down",
    headers: rowsFromRecord(config?.headers),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    const promql = state.mode === "promql";

    if (state.url) cfg.url = state.url;
    cfg.mode = state.mode || "scrape";

    if (promql) {
      if (state.query) cfg.query = state.query;
    } else {
      if (state.metric) cfg.metric = state.metric;
      const labels = recordFromRows(state.labels);
      if (Object.keys(labels).length > 0) cfg.labels = labels;
    }

    cfg.operator = state.operator || ">";

    // "" means unset; "0" is a real threshold and must survive.
    if (state.warningValue !== "")
      cfg.warningValue = parseFloat(state.warningValue);
    if (state.criticalValue !== "")
      cfg.criticalValue = parseFloat(state.criticalValue);

    if (state.match && state.match !== "single") cfg.match = state.match;
    if (state.onMissing && state.onMissing !== "down")
      cfg.onMissing = state.onMissing;

    const headers = recordFromRows(state.headers);
    if (Object.keys(headers).length > 0) cfg.headers = headers;

    const errors: FieldErrors = [];
    if (!state.url) errors.push({ name: "url", message: "URL is required" });
    if (promql && !state.query)
      errors.push({ name: "query", message: "PromQL query is required" });
    if (!promql && !state.metric)
      errors.push({ name: "metric", message: "Metric name is required" });
    if (state.warningValue === "" && state.criticalValue === "")
      errors.push({
        name: "criticalValue",
        message: "Set a warning and/or a critical threshold",
      });

    return { config: cfg, errors };
  },
  Fields: PrometheusFields,
};

// KeyValueRows renders an editable key/value list (labels, headers).
function KeyValueRows({
  rows,
  onRowsChange,
  keyPlaceholder,
  valuePlaceholder,
  addLabel,
  testIdPrefix,
}: {
  rows: PrometheusLabelRow[];
  onRowsChange: (next: PrometheusLabelRow[]) => void;
  keyPlaceholder: string;
  valuePlaceholder: string;
  addLabel: string;
  testIdPrefix: string;
}) {
  return (
    <div className="space-y-2">
      {rows.map((row, idx) => (
        <div key={idx} className="flex gap-2 items-center">
          <Input
            type="text"
            placeholder={keyPlaceholder}
            value={row.key}
            onChange={(e) => {
              const next = [...rows];
              next[idx] = { ...next[idx], key: e.target.value };
              onRowsChange(next);
            }}
            className="flex-1 min-w-0"
            data-testid={`${testIdPrefix}-key-${idx}`}
          />
          <Input
            type="text"
            placeholder={valuePlaceholder}
            value={row.value}
            onChange={(e) => {
              const next = [...rows];
              next[idx] = { ...next[idx], value: e.target.value };
              onRowsChange(next);
            }}
            className="flex-1 min-w-0"
            data-testid={`${testIdPrefix}-value-${idx}`}
          />
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="text-destructive shrink-0"
            onClick={() => onRowsChange(rows.filter((_, i) => i !== idx))}
            data-testid={`${testIdPrefix}-remove-${idx}`}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      ))}
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => onRowsChange([...rows, { key: "", value: "" }])}
        data-testid={`${testIdPrefix}-add`}
      >
        <Plus className="h-4 w-4 mr-1" />
        {addLabel}
      </Button>
    </div>
  );
}

function PrometheusFields({
  state,
  onChange,
  errors,
}: CheckTypeFieldsProps<PrometheusState>) {
  const promql = state.mode === "promql";
  const advancedCustomized =
    state.match !== "single" ||
    state.onMissing !== "down" ||
    state.headers.length > 0;

  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="prometheusMode">Mode</Label>
        <Select
          value={state.mode}
          onValueChange={(mode) => onChange({ ...state, mode })}
        >
          <SelectTrigger
            id="prometheusMode"
            data-testid="check-prometheus-mode-select"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="scrape">Scrape a /metrics endpoint</SelectItem>
            <SelectItem value="promql">
              PromQL query against a Prometheus server
            </SelectItem>
          </SelectContent>
        </Select>
      </div>

      <div className="space-y-2">
        <Label htmlFor="prometheusUrl">
          {promql ? "Prometheus server URL" : "Metrics URL"}
        </Label>
        <Input
          id="prometheusUrl"
          type="text"
          placeholder={
            promql
              ? "https://prometheus.example.com"
              : "https://app.example.com/metrics"
          }
          value={state.url}
          onChange={(e) => onChange({ ...state, url: e.target.value })}
          className={cn(getFieldError(errors, "url") && "border-destructive")}
          data-testid="check-prometheus-url-input"
        />
        {getFieldError(errors, "url") && (
          <p className="text-xs text-destructive">
            {getFieldError(errors, "url")}
          </p>
        )}
      </div>

      {promql ? (
        <div className="space-y-2">
          <Label htmlFor="prometheusQuery">PromQL query</Label>
          <Textarea
            id="prometheusQuery"
            rows={3}
            placeholder={'sum(rate(http_requests_total{code=~"5.."}[5m]))'}
            value={state.query}
            onChange={(e) => onChange({ ...state, query: e.target.value })}
            className={cn(
              "font-mono text-sm",
              getFieldError(errors, "query") && "border-destructive",
            )}
            data-testid="check-prometheus-query-input"
          />
          <p className="text-xs text-muted-foreground">
            Instant query. Scalar and instant-vector results are supported; a
            range (matrix) result is rejected. This is also where rates belong —
            the check does no client-side rate computation.
          </p>
          {getFieldError(errors, "query") && (
            <p className="text-xs text-destructive">
              {getFieldError(errors, "query")}
            </p>
          )}
        </div>
      ) : (
        <>
          <div className="space-y-2">
            <Label htmlFor="prometheusMetric">Metric</Label>
            <Input
              id="prometheusMetric"
              type="text"
              placeholder="process_open_fds"
              value={state.metric}
              onChange={(e) => onChange({ ...state, metric: e.target.value })}
              className={cn(
                "font-mono text-sm",
                getFieldError(errors, "metric") && "border-destructive",
              )}
              data-testid="check-prometheus-metric-input"
            />
            <p className="text-xs text-muted-foreground">
              Histograms and summaries are addressed through their flattened
              series: <code>_sum</code>, <code>_count</code>,{" "}
              <code>_bucket</code> (with an <code>le</code> label) or a{" "}
              <code>quantile</code> label.
            </p>
            {getFieldError(errors, "metric") && (
              <p className="text-xs text-destructive">
                {getFieldError(errors, "metric")}
              </p>
            )}
          </div>
          <div className="space-y-2">
            <Label>Labels (optional)</Label>
            <p className="text-xs text-muted-foreground">
              The series must carry every pair listed here. Extra labels on the
              series are fine.
            </p>
            <KeyValueRows
              rows={state.labels}
              onRowsChange={(labels) => onChange({ ...state, labels })}
              keyPlaceholder="instance"
              valuePlaceholder="app-1"
              addLabel="Add label"
              testIdPrefix="check-prometheus-label"
            />
          </div>
        </>
      )}

      <div className="space-y-2">
        <Label htmlFor="prometheusOperator">Alert when the value is</Label>
        <div className="flex flex-col sm:flex-row gap-2">
          <Select
            value={state.operator}
            onValueChange={(operator) => onChange({ ...state, operator })}
          >
            <SelectTrigger
              id="prometheusOperator"
              className="sm:w-40"
              data-testid="check-prometheus-operator-select"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value=">">&gt; greater than</SelectItem>
              <SelectItem value=">=">&ge; at least</SelectItem>
              <SelectItem value="<">&lt; less than</SelectItem>
              <SelectItem value="<=">&le; at most</SelectItem>
              <SelectItem value="==">= equal to</SelectItem>
              <SelectItem value="!=">&ne; not equal to</SelectItem>
            </SelectContent>
          </Select>
          <div className="flex-1 min-w-0 space-y-1">
            <Input
              id="prometheusWarningValue"
              type="number"
              step="any"
              placeholder="Warning threshold"
              value={state.warningValue}
              onChange={(e) =>
                onChange({ ...state, warningValue: e.target.value })
              }
              data-testid="check-prometheus-warning-input"
            />
          </div>
          <div className="flex-1 min-w-0 space-y-1">
            <Input
              id="prometheusCriticalValue"
              type="number"
              step="any"
              placeholder="Critical threshold"
              value={state.criticalValue}
              onChange={(e) =>
                onChange({ ...state, criticalValue: e.target.value })
              }
              className={cn(
                getFieldError(errors, "criticalValue") && "border-destructive",
              )}
              data-testid="check-prometheus-critical-input"
            />
          </div>
        </div>
        <p className="text-xs text-muted-foreground">
          Critical goes Down and pages; Warning is amber, counts as up and never
          pages. Set at least one — a warning-only check is valid. 0 is a real
          threshold. <code>=</code> and <code>&ne;</code> take a critical
          threshold only.
        </p>
        {getFieldError(errors, "criticalValue") && (
          <p className="text-xs text-destructive">
            {getFieldError(errors, "criticalValue")}
          </p>
        )}
      </div>

      <CollapsibleSection
        id="prometheus-advanced"
        title="Advanced"
        summary={`match ${state.match}, on missing ${state.onMissing}${
          state.headers.length > 0 ? `, ${state.headers.length} header(s)` : ""
        }`}
        customized={advancedCustomized}
        defaultOpen={advancedCustomized}
        data-testid="check-prometheus-advanced"
      >
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="prometheusMatch">Multiple matching series</Label>
            <Select
              value={state.match}
              onValueChange={(match) => onChange({ ...state, match })}
            >
              <SelectTrigger
                id="prometheusMatch"
                data-testid="check-prometheus-match-select"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="single">
                  Single — error if more than one matches
                </SelectItem>
                <SelectItem value="min">Minimum</SelectItem>
                <SelectItem value="max">Maximum</SelectItem>
                <SelectItem value="sum">Sum</SelectItem>
                <SelectItem value="avg">Average</SelectItem>
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground">
              An ambiguous selector is reported rather than resolved by guessing.
            </p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="prometheusOnMissing">When nothing matches</Label>
            <Select
              value={state.onMissing}
              onValueChange={(onMissing) => onChange({ ...state, onMissing })}
            >
              <SelectTrigger
                id="prometheusOnMissing"
                data-testid="check-prometheus-onmissing-select"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="down">Down (default)</SelectItem>
                <SelectItem value="warning">Warning</SelectItem>
                <SelectItem value="up">Up</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-2">
            <Label>Request headers</Label>
            <p className="text-xs text-muted-foreground">
              For endpoints behind bearer or basic auth.
            </p>
            <KeyValueRows
              rows={state.headers}
              onRowsChange={(headers) => onChange({ ...state, headers })}
              keyPlaceholder="Authorization"
              valuePlaceholder="Bearer …"
              addLabel="Add header"
              testIdPrefix="check-prometheus-header"
            />
          </div>
        </div>
      </CollapsibleSection>
    </>
  );
}
