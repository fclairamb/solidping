import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "@tanstack/react-router";
import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
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
