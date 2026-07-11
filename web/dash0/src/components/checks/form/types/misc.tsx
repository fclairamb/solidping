import { useTranslation } from "react-i18next";
import CodeMirror from "@uiw/react-codemirror";
import { javascript } from "@codemirror/lang-javascript";
import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Checkbox } from "@/components/ui/checkbox";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { getFieldError } from "@/hooks/use-check-validation";
import { useEmailAddressDomain } from "@/api/email-inbox";
import type { CheckTypeModule } from "./index";
import type { CheckConfig, CheckTypeFieldsProps, FieldErrors } from "./common";
import { getConfigField } from "./common";
import { useCheckFormFields } from "./context";

const hostRequired = (host: string): FieldErrors =>
  host ? [] : [{ name: "host", message: "Host is required" }];

// ── SSL ──
export interface SslState {
  host: string;
  port: string;
  serverName: string;
  criticalDays: string;
  warningDays: string;
}

export const sslModule: CheckTypeModule<SslState> = {
  types: ["ssl"],
  fromConfig: (config) => ({
    host: getConfigField(config, "host"),
    port: getConfigField(config, "port"),
    serverName:
      getConfigField(config, "serverName") ||
      getConfigField(config, "server_name"),
    criticalDays:
      getConfigField(config, "criticalDays") ||
      getConfigField(config, "thresholdDays") ||
      getConfigField(config, "threshold_days"),
    warningDays:
      getConfigField(config, "warningDays") ||
      getConfigField(config, "warning_days"),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.serverName) cfg.serverName = state.serverName;
    if (state.criticalDays) cfg.criticalDays = parseInt(state.criticalDays, 10);
    if (state.warningDays) cfg.warningDays = parseInt(state.warningDays, 10);
    return { config: cfg, errors: hostRequired(state.host) };
  },
  Fields: SslFields,
};

function SslFields({ state, onChange, errors }: CheckTypeFieldsProps<SslState>) {
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
        <div className="flex gap-2">
          <Input
            id="host"
            type="text"
            placeholder="example.com"
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
            placeholder="443"
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
        <Label htmlFor="serverName">Server Name (SNI, optional)</Label>
        <Input
          id="serverName"
          type="text"
          placeholder="defaults to host"
          value={state.serverName}
          onChange={(e) => onChange({ ...state, serverName: e.target.value })}
          data-testid="check-server-name-input"
        />
      </div>
      <div className="flex gap-4">
        <div className="space-y-2 w-40">
          <Label htmlFor="criticalDays">Critical (days)</Label>
          <Input
            id="criticalDays"
            type="number"
            placeholder="30"
            value={state.criticalDays}
            onChange={(e) => onChange({ ...state, criticalDays: e.target.value })}
            data-testid="check-critical-days-input"
          />
          <p className="text-xs text-muted-foreground">
            Down (pages) at or below this.
          </p>
        </div>
        <div className="space-y-2 w-40">
          <Label htmlFor="warningDays">Warning (days)</Label>
          <Input
            id="warningDays"
            type="number"
            placeholder="30"
            value={state.warningDays}
            onChange={(e) => onChange({ ...state, warningDays: e.target.value })}
            data-testid="check-warning-days-input"
          />
          <p className="text-xs text-muted-foreground">
            Amber warning (no page) at or below this. Must be ≥ Critical.
          </p>
        </div>
      </div>
    </>
  );
}

// ── NTP ──
export interface NtpState {
  host: string;
  port: string;
  version: string;
  offsetWarnMs: string;
  offsetCritMs: string;
  maxStratum: string;
}

export const ntpModule: CheckTypeModule<NtpState> = {
  types: ["ntp"],
  fromConfig: (config) => ({
    host: getConfigField(config, "host"),
    port: getConfigField(config, "port"),
    version: String(getConfigField(config, "version") || "4"),
    offsetWarnMs: getConfigField(config, "offset_warn_ms"),
    offsetCritMs: getConfigField(config, "offset_crit_ms"),
    maxStratum: getConfigField(config, "max_stratum"),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.version) cfg.version = parseInt(state.version, 10);
    if (state.offsetWarnMs)
      cfg.offset_warn_ms = parseInt(state.offsetWarnMs, 10);
    if (state.offsetCritMs)
      cfg.offset_crit_ms = parseInt(state.offsetCritMs, 10);
    if (state.maxStratum) cfg.max_stratum = parseInt(state.maxStratum, 10);
    return { config: cfg, errors: hostRequired(state.host) };
  },
  Fields: NtpFields,
};

function NtpFields({ state, onChange, errors }: CheckTypeFieldsProps<NtpState>) {
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
        <div className="flex gap-2">
          <Input
            id="host"
            type="text"
            placeholder="pool.ntp.org"
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
            placeholder="123"
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
      <div className="space-y-2 w-40">
        <Label htmlFor="ntpVersion">Version</Label>
        <Select
          value={state.version}
          onValueChange={(version) => onChange({ ...state, version })}
        >
          <SelectTrigger data-testid="check-ntp-version-select">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="4">4</SelectItem>
            <SelectItem value="3">3</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div className="flex gap-4">
        <div className="space-y-2 w-40">
          <Label htmlFor="ntpOffsetCritMs">Offset critical (ms)</Label>
          <Input
            id="ntpOffsetCritMs"
            type="number"
            min={0}
            placeholder="off"
            value={state.offsetCritMs}
            onChange={(e) => onChange({ ...state, offsetCritMs: e.target.value })}
            data-testid="check-ntp-offset-crit-input"
          />
          <p className="text-xs text-muted-foreground">
            Down (pages) when |offset| exceeds this. Worker-relative.
          </p>
        </div>
        <div className="space-y-2 w-40">
          <Label htmlFor="ntpOffsetWarnMs">Offset warning (ms)</Label>
          <Input
            id="ntpOffsetWarnMs"
            type="number"
            min={0}
            placeholder="off"
            value={state.offsetWarnMs}
            onChange={(e) => onChange({ ...state, offsetWarnMs: e.target.value })}
            data-testid="check-ntp-offset-warn-input"
          />
          <p className="text-xs text-muted-foreground">
            Amber (no page) when |offset| exceeds this. Must be ≤ Critical.
          </p>
        </div>
      </div>
      <div className="space-y-2 w-40">
        <Label htmlFor="ntpMaxStratum">Max stratum (optional)</Label>
        <Input
          id="ntpMaxStratum"
          type="number"
          min={1}
          max={15}
          placeholder="off"
          value={state.maxStratum}
          onChange={(e) => onChange({ ...state, maxStratum: e.target.value })}
          data-testid="check-ntp-max-stratum-input"
        />
        <p className="text-xs text-muted-foreground">
          Down when the server's stratum exceeds this (1–15).
        </p>
      </div>
    </>
  );
}

// ── RDP ──
export interface RdpState {
  host: string;
  port: string;
  requireNLA: boolean;
  warningDays: string;
  criticalDays: string;
}

export const rdpModule: CheckTypeModule<RdpState> = {
  types: ["rdp"],
  fromConfig: (config) => ({
    host: getConfigField(config, "host"),
    port: getConfigField(config, "port"),
    requireNLA: getConfigField(config, "require_nla") === "true",
    warningDays: getConfigField(config, "warning_days"),
    criticalDays: getConfigField(config, "critical_days"),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.requireNLA) cfg.require_nla = true;
    if (state.warningDays) cfg.warning_days = parseInt(state.warningDays, 10);
    if (state.criticalDays) cfg.critical_days = parseInt(state.criticalDays, 10);
    return { config: cfg, errors: hostRequired(state.host) };
  },
  Fields: RdpFields,
};

function RdpFields({ state, onChange, errors }: CheckTypeFieldsProps<RdpState>) {
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
        <div className="flex gap-2">
          <Input
            id="host"
            type="text"
            placeholder="rdp.example.internal"
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
            placeholder="3389"
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
        <label className="flex items-center gap-2">
          <Checkbox
            checked={state.requireNLA}
            onCheckedChange={(v) => onChange({ ...state, requireNLA: v === true })}
            data-testid="check-rdp-require-nla-checkbox"
          />
          <span className="text-sm">
            Require NLA (Network Level Authentication)
          </span>
        </label>
        <p className="text-xs text-muted-foreground">
          Down when the server does not select CredSSP — catches NLA silently
          disabled by policy.
        </p>
      </div>
      <div className="flex gap-4">
        <div className="space-y-2 w-40">
          <Label htmlFor="rdpCriticalDays">Cert critical (days)</Label>
          <Input
            id="rdpCriticalDays"
            type="number"
            min={0}
            placeholder="off"
            value={state.criticalDays}
            onChange={(e) => onChange({ ...state, criticalDays: e.target.value })}
            data-testid="check-rdp-critical-days-input"
          />
          <p className="text-xs text-muted-foreground">
            Down (pages) when the certificate expires in at most this many days.
          </p>
        </div>
        <div className="space-y-2 w-40">
          <Label htmlFor="rdpWarningDays">Cert warning (days)</Label>
          <Input
            id="rdpWarningDays"
            type="number"
            min={0}
            placeholder="off"
            value={state.warningDays}
            onChange={(e) => onChange({ ...state, warningDays: e.target.value })}
            data-testid="check-rdp-warning-days-input"
          />
          <p className="text-xs text-muted-foreground">
            Amber warning (no page). Must be ≥ Critical.
          </p>
        </div>
      </div>
      <p className="text-xs text-muted-foreground">
        Pre-auth handshake only — no credentials are sent. Workers need network
        access to the RDP host (typically internal).
      </p>
    </>
  );
}

// ── SIP ──
export interface SipState {
  host: string;
  port: string;
  transport: string;
  mode: string;
  domain: string;
  username: string;
  password: string;
  expectStatus: string;
}

export const sipModule: CheckTypeModule<SipState> = {
  types: ["sip"],
  fromConfig: (config) => ({
    host: getConfigField(config, "host"),
    port: getConfigField(config, "port"),
    transport: getConfigField(config, "transport") || "udp",
    mode: getConfigField(config, "mode") || "options",
    domain: getConfigField(config, "domain"),
    username: getConfigField(config, "username"),
    password: getConfigField(config, "password"),
    expectStatus: getConfigField(config, "expect_status"),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.transport && state.transport !== "udp")
      cfg.transport = state.transport;
    if (state.mode && state.mode !== "options") cfg.mode = state.mode;
    if (state.domain) cfg.domain = state.domain;
    if (state.mode === "register") {
      if (state.username) cfg.username = state.username;
      if (state.password) cfg.password = state.password;
    }
    if (state.mode === "options" && state.expectStatus)
      cfg.expect_status = state.expectStatus;
    const errors: FieldErrors = [];
    if (!state.host) errors.push({ name: "host", message: "Host is required" });
    else if (state.mode === "register") {
      if (!state.username)
        errors.push({
          name: "username",
          message: "Username is required for register mode",
        });
      if (!state.password)
        errors.push({
          name: "password",
          message: "Password is required for register mode",
        });
    }
    return { config: cfg, errors };
  },
  Fields: SipFields,
};

function SipFields({ state, onChange }: CheckTypeFieldsProps<SipState>) {
  const { t } = useTranslation("checks");
  const { configPrivateKeys } = useCheckFormFields();
  return (
    <>
      <div className="space-y-2">
        <Label>{t("form.host")}</Label>
        <div className="flex gap-2">
          <Input
            id="host"
            type="text"
            placeholder="pbx.example.com"
            value={state.host}
            onChange={(e) => onChange({ ...state, host: e.target.value })}
            className="flex-1"
            data-testid="check-sip-host-input"
          />
          <Input
            id="port"
            type="number"
            placeholder={state.transport === "tls" ? "5061" : "5060"}
            value={state.port}
            onChange={(e) => onChange({ ...state, port: e.target.value })}
            className="w-24"
            data-testid="check-sip-port-input"
          />
        </div>
      </div>
      <div className="flex gap-4">
        <div className="space-y-2 flex-1">
          <Label htmlFor="sipTransport">{t("sip.transport")}</Label>
          <Select
            value={state.transport}
            onValueChange={(transport) => onChange({ ...state, transport })}
          >
            <SelectTrigger id="sipTransport" data-testid="check-sip-transport-select">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="udp">UDP</SelectItem>
              <SelectItem value="tcp">TCP</SelectItem>
              <SelectItem value="tls">TLS</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-2 flex-1">
          <Label htmlFor="sipMode">{t("sip.mode")}</Label>
          <Select
            value={state.mode}
            onValueChange={(mode) => onChange({ ...state, mode })}
          >
            <SelectTrigger id="sipMode" data-testid="check-sip-mode-select">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="options">{t("sip.modeOptions")}</SelectItem>
              <SelectItem value="register">{t("sip.modeRegister")}</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>
      <div className="space-y-2">
        <Label htmlFor="domain">{t("sip.domain")}</Label>
        <Input
          id="domain"
          type="text"
          placeholder="defaults to host"
          value={state.domain}
          onChange={(e) => onChange({ ...state, domain: e.target.value })}
          data-testid="check-sip-domain-input"
        />
        <p className="text-xs text-muted-foreground">{t("sip.domainHelp")}</p>
      </div>
      {state.mode === "register" ? (
        <div className="flex gap-4">
          <div className="space-y-2 flex-1">
            <Label htmlFor="username">{t("sip.username")}</Label>
            <Input
              id="username"
              type="text"
              placeholder="1001"
              value={state.username}
              onChange={(e) => onChange({ ...state, username: e.target.value })}
              data-testid="check-sip-username-input"
            />
          </div>
          <div className="space-y-2 flex-1">
            <Label htmlFor="password">{t("sip.password")}</Label>
            <Input
              id="password"
              type="password"
              value={state.password}
              onChange={(e) => onChange({ ...state, password: e.target.value })}
              data-testid="check-sip-password-input"
            />
            {configPrivateKeys?.includes("password") && !state.password && (
              <p className="text-xs text-muted-foreground">
                <span className="font-mono tracking-widest">••••</span>{" "}
                <span className="italic">{t("sip.passwordEncrypted")}</span>
              </p>
            )}
          </div>
        </div>
      ) : (
        <div className="space-y-2">
          <Label htmlFor="sipExpectStatus">{t("sip.expectStatus")}</Label>
          <Input
            id="sipExpectStatus"
            type="text"
            placeholder="200,405"
            value={state.expectStatus}
            onChange={(e) => onChange({ ...state, expectStatus: e.target.value })}
            data-testid="check-sip-expect-status-input"
          />
          <p className="text-xs text-muted-foreground">
            {t("sip.expectStatusHelp")}
          </p>
        </div>
      )}
    </>
  );
}

// ── JavaScript ──
export interface JsState {
  script: string;
}

export const jsModule: CheckTypeModule<JsState> = {
  types: ["js"],
  fromConfig: (config) => ({ script: getConfigField(config, "script") }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.script) cfg.script = state.script;
    const errors: FieldErrors = state.script
      ? []
      : [{ name: "script", message: "Script is required" }];
    return { config: cfg, errors };
  },
  Fields: JsFields,
};

function JsFields({ state, onChange, errors }: CheckTypeFieldsProps<JsState>) {
  return (
    <div className="space-y-2">
      <Label htmlFor="script">Script</Label>
      <CodeMirror
        value={state.script}
        onChange={(value) => onChange({ ...state, script: value })}
        extensions={[javascript()]}
        theme={
          document.documentElement.classList.contains("dark") ? "dark" : "light"
        }
        height="200px"
        className={cn(
          "rounded-md border text-sm",
          getFieldError(errors, "script") && "border-destructive",
        )}
        data-testid="check-script"
      />
      {getFieldError(errors, "script") && (
        <p className="text-xs text-destructive">
          {getFieldError(errors, "script")}
        </p>
      )}
      <p className="text-xs text-muted-foreground">
        JavaScript script that returns an object with status (&quot;up&quot;,
        &quot;down&quot;, or &quot;error&quot;), optional metrics, and optional
        output.
      </p>
    </div>
  );
}

// ── Sleep (synthetic) ──
export interface SleepState {
  sleepMs: string;
  jitterMs: string;
  status: string;
}

export const sleepModule: CheckTypeModule<SleepState> = {
  types: ["sleep"],
  fromConfig: (config) => ({
    sleepMs: getConfigField(config, "sleep_ms"),
    jitterMs: getConfigField(config, "jitter_ms"),
    status: getConfigField(config, "status") || "up",
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.sleepMs) cfg.sleep_ms = parseInt(state.sleepMs, 10);
    if (state.jitterMs) cfg.jitter_ms = parseInt(state.jitterMs, 10);
    if (state.status && state.status !== "up") cfg.status = state.status;
    const errors: FieldErrors = state.sleepMs
      ? []
      : [{ name: "sleep_ms", message: "Sleep duration is required" }];
    return { config: cfg, errors };
  },
  Fields: SleepFields,
};

function SleepFields({ state, onChange, errors }: CheckTypeFieldsProps<SleepState>) {
  return (
    <>
      <Alert>
        <AlertDescription className="text-xs">
          Synthetic checker — sleeps for the configured duration and performs no
          network I/O. Useful for testing scheduler/load behavior, not a real
          availability probe.
        </AlertDescription>
      </Alert>
      <div className="flex gap-4">
        <div className="space-y-2 w-40">
          <Label htmlFor="sleepMs">Sleep duration (ms)</Label>
          <Input
            id="sleepMs"
            type="number"
            min={1}
            placeholder="500"
            value={state.sleepMs}
            onChange={(e) => onChange({ ...state, sleepMs: e.target.value })}
            className={cn(getFieldError(errors, "sleep_ms") && "border-destructive")}
            data-testid="check-sleep-ms-input"
          />
          {getFieldError(errors, "sleep_ms") && (
            <p className="text-xs text-destructive">
              {getFieldError(errors, "sleep_ms")}
            </p>
          )}
        </div>
        <div className="space-y-2 w-40">
          <Label htmlFor="jitterMs">Jitter (ms, optional)</Label>
          <Input
            id="jitterMs"
            type="number"
            min={0}
            placeholder="0"
            value={state.jitterMs}
            onChange={(e) => onChange({ ...state, jitterMs: e.target.value })}
            className={cn(getFieldError(errors, "jitter_ms") && "border-destructive")}
            data-testid="check-jitter-ms-input"
          />
          {getFieldError(errors, "jitter_ms") && (
            <p className="text-xs text-destructive">
              {getFieldError(errors, "jitter_ms")}
            </p>
          )}
        </div>
      </div>
      <p className="text-xs text-muted-foreground">
        ± random variation applied to the sleep duration. Must be less than the
        sleep duration itself.
      </p>
      <div className="space-y-2 w-40">
        <Label htmlFor="sleepStatus">Forced status (optional)</Label>
        <Select
          value={state.status}
          onValueChange={(status) => onChange({ ...state, status })}
        >
          <SelectTrigger id="sleepStatus" data-testid="check-sleep-status-select">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="up">Up (default)</SelectItem>
            <SelectItem value="down">Down</SelectItem>
            <SelectItem value="timeout">Timeout</SelectItem>
            <SelectItem value="error">Error</SelectItem>
          </SelectContent>
        </Select>
        {getFieldError(errors, "status") && (
          <p className="text-xs text-destructive">
            {getFieldError(errors, "status")}
          </p>
        )}
      </div>
    </>
  );
}

// ── Heartbeat (passive) ──
export type EmptyState = Record<string, never>;

export const heartbeatModule: CheckTypeModule<EmptyState> = {
  types: ["heartbeat"],
  fromConfig: () => ({}),
  toConfig: () => ({ config: {}, errors: [] }),
  Fields: () => (
    <p className="text-sm text-muted-foreground">
      No additional configuration needed. A heartbeat URL will be generated after
      creation.
    </p>
  ),
};

// ── Email (passive) ──
export const emailModule: CheckTypeModule<EmptyState> = {
  types: ["email"],
  fromConfig: () => ({}),
  toConfig: () => ({ config: {}, errors: [] }),
  Fields: EmailFields,
};

function EmailFields() {
  const { data: emailDomain } = useEmailAddressDomain();
  if (!emailDomain) {
    return (
      <Alert variant="destructive">
        <AlertDescription>
          Email inbox not configured. Ask your administrator to set it up under
          Server &rarr; Email Inbox.
        </AlertDescription>
      </Alert>
    );
  }
  return (
    <p className="text-sm text-muted-foreground">
      An email address will be generated for this check. Send any email to that
      address to report a successful run. Use plus-addressing (
      <code className="font-mono">token+down@…</code>) or{" "}
      <code className="font-mono">[DOWN]</code> in the subject to report failure.
    </p>
  );
}
