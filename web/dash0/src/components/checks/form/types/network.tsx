import { useState } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { getFieldError } from "@/hooks/use-check-validation";
import { canSource } from "@/api/hooks";
import type { FreeboxLanHost } from "@/api/hooks";
import { FreeboxLanDiscovery } from "@/components/shared/freebox-lan-discovery";
import type { CheckTypeModule } from "./index";
import type { CheckConfig, CheckTypeFieldsProps, FieldErrors } from "./common";
import { getConfigField } from "./common";
import { useCheckFormFields } from "./context";

const hostRequired = (host: string): FieldErrors =>
  host ? [] : [{ name: "host", message: "Host is required" }];

// ── TCP / UDP ──
export interface HostPortState {
  host: string;
  port: string;
}

export const tcpModule: CheckTypeModule<HostPortState> = {
  types: ["tcp", "udp"],
  fromConfig: (config) => ({
    host: getConfigField(config, "host"),
    port: getConfigField(config, "port"),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    return { config: cfg, errors: hostRequired(state.host) };
  },
  Fields: TcpFields,
};

function TcpFields({
  state,
  onChange,
  errors,
}: CheckTypeFieldsProps<HostPortState>) {
  const { type } = useCheckFormFields();
  return (
    <div className="space-y-2">
      <Label>Host</Label>
      <div className="flex gap-2">
        <Input
          id="host"
          type="text"
          placeholder={type === "udp" ? "8.8.8.8" : "example.com"}
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
          placeholder={type === "udp" ? "53" : "443"}
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
  );
}

// ── SSH ──
export interface HostPortUserPassState {
  host: string;
  port: string;
  username: string;
  password: string;
}

const hostPortUserPassFromConfig = (
  config: CheckConfig,
): HostPortUserPassState => ({
  host: getConfigField(config, "host"),
  port: getConfigField(config, "port"),
  username: getConfigField(config, "username"),
  password: getConfigField(config, "password"),
});

export const sshModule: CheckTypeModule<HostPortUserPassState> = {
  types: ["ssh"],
  fromConfig: hostPortUserPassFromConfig,
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.username) cfg.username = state.username;
    if (state.password) cfg.password = state.password;
    return { config: cfg, errors: hostRequired(state.host) };
  },
  Fields: SshFields,
};

function SshFields({
  state,
  onChange,
  errors,
}: CheckTypeFieldsProps<HostPortUserPassState>) {
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
        <div className="flex gap-2">
          <Input
            id="host"
            type="text"
            placeholder="server.example.com"
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
            placeholder="22"
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
        <Label htmlFor="username">Username (optional)</Label>
        <Input
          id="username"
          type="text"
          placeholder="user"
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
    </>
  );
}

// ── SFTP ──
export const sftpModule: CheckTypeModule<HostPortUserPassState> = {
  types: ["sftp"],
  fromConfig: hostPortUserPassFromConfig,
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.username) cfg.username = state.username;
    if (state.password) cfg.password = state.password;
    return { config: cfg, errors: hostRequired(state.host) };
  },
  Fields: SftpFields,
};

function SftpFields({
  state,
  onChange,
}: CheckTypeFieldsProps<HostPortUserPassState>) {
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
        <div className="flex gap-2">
          <Input
            id="host"
            type="text"
            placeholder="sftp.example.com"
            value={state.host}
            onChange={(e) => onChange({ ...state, host: e.target.value })}
            className="flex-1"
            data-testid="check-host-input"
          />
          <Input
            id="port"
            type="number"
            placeholder="22"
            value={state.port}
            onChange={(e) => onChange({ ...state, port: e.target.value })}
            className="w-24"
            data-testid="check-port-input"
          />
        </div>
      </div>
      <div className="space-y-2">
        <Label htmlFor="username">Username</Label>
        <Input
          id="username"
          type="text"
          placeholder="user"
          value={state.username}
          onChange={(e) => onChange({ ...state, username: e.target.value })}
          data-testid="check-username-input"
        />
      </div>
      <div className="space-y-2">
        <Label htmlFor="password">Password</Label>
        <Input
          id="password"
          type="password"
          value={state.password}
          onChange={(e) => onChange({ ...state, password: e.target.value })}
          data-testid="check-password-input"
        />
      </div>
    </>
  );
}

// ── FTP ──
export const ftpModule: CheckTypeModule<HostPortUserPassState> = {
  types: ["ftp"],
  fromConfig: hostPortUserPassFromConfig,
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    cfg.username = state.username || "anonymous";
    if (state.password) cfg.password = state.password;
    return { config: cfg, errors: hostRequired(state.host) };
  },
  Fields: FtpFields,
};

function FtpFields({
  state,
  onChange,
}: CheckTypeFieldsProps<HostPortUserPassState>) {
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
        <div className="flex gap-2">
          <Input
            id="host"
            type="text"
            placeholder="ftp.example.com"
            value={state.host}
            onChange={(e) => onChange({ ...state, host: e.target.value })}
            className="flex-1"
            data-testid="check-host-input"
          />
          <Input
            id="port"
            type="number"
            placeholder="21"
            value={state.port}
            onChange={(e) => onChange({ ...state, port: e.target.value })}
            className="w-24"
            data-testid="check-port-input"
          />
        </div>
      </div>
      <div className="space-y-2">
        <Label htmlFor="username">Username (optional, default: anonymous)</Label>
        <Input
          id="username"
          type="text"
          placeholder="anonymous"
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
    </>
  );
}

// ── ICMP ──
export interface IcmpState {
  host: string;
}

export const icmpModule: CheckTypeModule<IcmpState> = {
  types: ["icmp"],
  fromConfig: (config) => ({ host: getConfigField(config, "host") }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    return { config: cfg, errors: hostRequired(state.host) };
  },
  Fields: IcmpFields,
};

function IcmpFields({ state, onChange, errors }: CheckTypeFieldsProps<IcmpState>) {
  const { t } = useTranslation("checks");
  const { org, connections, name, setName } = useCheckFormFields();
  const [discoverOpen, setDiscoverOpen] = useState(false);
  // Source integrations (canSource) drive the LAN-host discovery picker. Today
  // this is freebox only; the `granted` gate is the Freebox pairing state.
  const freeboxChannels = (connections ?? []).filter(
    (c) =>
      canSource(c.type) &&
      (c.settings?.status as string | undefined) === "granted",
  );
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <Label htmlFor="host">Host</Label>
        {freeboxChannels.length > 0 && (
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => setDiscoverOpen(true)}
            data-testid="check-freebox-discover-button"
          >
            {t("freebox.discover")}
          </Button>
        )}
      </div>
      <Input
        id="host"
        type="text"
        placeholder="example.com"
        value={state.host}
        onChange={(e) => onChange({ ...state, host: e.target.value })}
        className={cn(getFieldError(errors, "host") && "border-destructive")}
        data-testid="check-host-input"
      />
      {getFieldError(errors, "host") && (
        <p className="text-xs text-destructive">
          {getFieldError(errors, "host")}
        </p>
      )}
      {freeboxChannels.length > 0 && (
        <FreeboxLanDiscovery
          org={org}
          open={discoverOpen}
          onOpenChange={setDiscoverOpen}
          channels={freeboxChannels}
          onSelect={(picked: FreeboxLanHost) => {
            onChange({ ...state, host: picked.ip });
            if (!name) {
              setName(picked.name);
            }
          }}
        />
      )}
    </div>
  );
}
