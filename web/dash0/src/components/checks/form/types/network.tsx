import { useState } from "react";
import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { CollapsibleSection } from "@/components/ui/collapsible-section";
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

/** How `send_data` / `expect_data` are decoded into bytes by the checker. */
export type PayloadEncoding = "text" | "escaped" | "hex";
export const PAYLOAD_ENCODINGS: readonly PayloadEncoding[] = [
  "text",
  "escaped",
  "hex",
];

/**
 * `contains` writes `expect_data` (+ `expect_encoding`), `regex` writes
 * `expect_pattern`. They are two config keys behind one input, so the mode is
 * part of the form state and switching it must DELETE the other key — which
 * works because both are in `ownedKeys` (the omit-to-clear contract).
 */
export type ExpectMode = "contains" | "regex";

export interface HostPortState {
  host: string;
  port: string;
  sendData: string;
  sendEncoding: PayloadEncoding;
  expectMode: ExpectMode;
  expectValue: string;
  expectEncoding: PayloadEncoding;
}

const asEncoding = (raw: string, fallback: PayloadEncoding): PayloadEncoding =>
  (PAYLOAD_ENCODINGS as readonly string[]).includes(raw)
    ? (raw as PayloadEncoding)
    : fallback;

export const tcpModule: CheckTypeModule<HostPortState> = {
  types: ["tcp", "udp"],
  ownedKeys: [
    "host",
    "port",
    "send_data",
    "send_encoding",
    "expect_data",
    "expect_encoding",
    "expect_pattern",
  ],
  fromConfig: (config) => {
    const sendData = getConfigField(config, "send_data");
    const expectPattern = getConfigField(config, "expect_pattern");
    const expectData = getConfigField(config, "expect_data");
    return {
      host: getConfigField(config, "host"),
      port: getConfigField(config, "port"),
      sendData,
      // A STORED payload with no explicit encoding is `text` — that is the
      // backend default and changing it would change what the check sends.
      // With nothing stored there is nothing to preserve, so a new check gets
      // `escaped`: a <textarea> cannot produce a CR any other way.
      sendEncoding: asEncoding(
        getConfigField(config, "send_encoding"),
        sendData ? "text" : "escaped",
      ),
      expectMode: expectPattern ? "regex" : "contains",
      expectValue: expectPattern || expectData,
      expectEncoding: asEncoding(
        getConfigField(config, "expect_encoding"),
        "text",
      ),
    };
  },
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.sendData) {
      cfg.send_data = state.sendData;
      // `text` is the backend default; writing it out would be noise.
      if (state.sendEncoding !== "text") cfg.send_encoding = state.sendEncoding;
    }
    if (state.expectValue) {
      if (state.expectMode === "regex") {
        cfg.expect_pattern = state.expectValue;
      } else {
        cfg.expect_data = state.expectValue;
        if (state.expectEncoding !== "text") {
          cfg.expect_encoding = state.expectEncoding;
        }
      }
    }
    return { config: cfg, errors: hostRequired(state.host) };
  },
  Fields: TcpFields,
};

function EncodingSelect({
  id,
  value,
  onValueChange,
  testId,
}: {
  id: string;
  value: PayloadEncoding;
  onValueChange: (value: PayloadEncoding) => void;
  testId: string;
}) {
  const { t } = useTranslation("checks");
  return (
    <Select
      value={value}
      onValueChange={(next) => onValueChange(next as PayloadEncoding)}
    >
      <SelectTrigger id={id} className="w-32" data-testid={testId}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {PAYLOAD_ENCODINGS.map((encoding) => (
          <SelectItem key={encoding} value={encoding}>
            {t(`network.encoding_${encoding}`)}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

function TcpFields({
  state,
  onChange,
  errors,
}: CheckTypeFieldsProps<HostPortState>) {
  const { t } = useTranslation("checks");
  const { type } = useCheckFormFields();
  return (
    <>
      <div className="space-y-2">
        <Label>{t("form.host")}</Label>
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
      <CollapsibleSection
        title={t("network.payloadReply")}
        summary={t("network.payloadReplySummary")}
        customized={Boolean(state.sendData || state.expectValue)}
        defaultOpen={Boolean(state.sendData || state.expectValue)}
        data-testid="check-payload-section"
      >
        <div className="space-y-2">
          <Label htmlFor="sendData">{t("network.sendOptional")}</Label>
          <div className="flex gap-2">
            <Textarea
              id="sendData"
              rows={2}
              placeholder={
                type === "udp" ? "5350 0100 0001" : String.raw`PING\r\n`
              }
              value={state.sendData}
              onChange={(e) => onChange({ ...state, sendData: e.target.value })}
              className="flex-1 font-mono text-xs"
              data-testid="check-send-data-input"
            />
            <EncodingSelect
              id="sendEncoding"
              value={state.sendEncoding}
              onValueChange={(sendEncoding) =>
                onChange({ ...state, sendEncoding })
              }
              testId="check-send-encoding-select"
            />
          </div>
        </div>
        <div className="space-y-2">
          <Label htmlFor="expectValue">{t("network.expectOptional")}</Label>
          <div className="flex gap-2">
            <Select
              value={state.expectMode}
              onValueChange={(mode) =>
                onChange({ ...state, expectMode: mode as ExpectMode })
              }
            >
              <SelectTrigger
                id="expectMode"
                className="w-40"
                data-testid="check-expect-mode-select"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="contains">
                  {t("network.expectModeContains")}
                </SelectItem>
                <SelectItem value="regex">
                  {t("network.expectModeRegex")}
                </SelectItem>
              </SelectContent>
            </Select>
            <Input
              id="expectValue"
              type="text"
              placeholder={state.expectMode === "regex" ? "^\\+PONG" : "+PONG"}
              value={state.expectValue}
              onChange={(e) =>
                onChange({ ...state, expectValue: e.target.value })
              }
              className="flex-1 font-mono text-xs"
              data-testid="check-expect-input"
            />
            {state.expectMode === "contains" && (
              <EncodingSelect
                id="expectEncoding"
                value={state.expectEncoding}
                onValueChange={(expectEncoding) =>
                  onChange({ ...state, expectEncoding })
                }
                testId="check-expect-encoding-select"
              />
            )}
          </div>
        </div>
        <p className="text-xs text-muted-foreground">
          {t("network.payloadHelp")}
          {type === "udp" ? ` ${t("network.payloadHelpUdp")}` : ""}
        </p>
      </CollapsibleSection>
    </>
  );
}

// ── SSH ──
export interface HostPortUserPassState {
  host: string;
  port: string;
  username: string;
  password: string;
  privateKey: string;
  // Only meaningful for SSH: the host key an SSH check must present to be
  // used as a tunnel bastion (see `expected_fingerprint`, checkerdef/tunnel.go
  // and sshtunnel.go). Carried on the shared state so it round-trips through
  // `hostPortUserPassFromConfig`, but only `sshModule` renders or serializes
  // it — SFTP/FTP checks have no concept of a bastion.
  expectedFingerprint: string;
}

const hostPortUserPassFromConfig = (
  config: CheckConfig,
): HostPortUserPassState => ({
  host: getConfigField(config, "host"),
  port: getConfigField(config, "port"),
  username: getConfigField(config, "username"),
  password: getConfigField(config, "password"),
  privateKey: getConfigField(config, "private_key"),
  expectedFingerprint: getConfigField(config, "expected_fingerprint"),
});

export const sshModule: CheckTypeModule<HostPortUserPassState> = {
  types: ["ssh"],
  ownedKeys: [
    "host",
    "port",
    "username",
    "password",
    "private_key",
    "expected_fingerprint",
  ],
  fromConfig: hostPortUserPassFromConfig,
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.username) cfg.username = state.username;
    if (state.password) cfg.password = state.password;
    if (state.privateKey) cfg.private_key = state.privateKey;
    if (state.expectedFingerprint) {
      cfg.expected_fingerprint = state.expectedFingerprint;
    }
    return { config: cfg, errors: hostRequired(state.host) };
  },
  Fields: SshFields,
};

function SshFields({
  state,
  onChange,
  errors,
}: CheckTypeFieldsProps<HostPortUserPassState>) {
  const { t } = useTranslation("checks");
  const { configPrivateKeys } = useCheckFormFields();
  const hasStoredKey = configPrivateKeys?.includes("private_key") ?? false;
  return (
    <>
      <div className="space-y-2">
        <Label>{t("form.host")}</Label>
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
        <Label htmlFor="expected-fingerprint">
          {t("network.hostKeyFingerprintOptional")}
        </Label>
        <Input
          id="expected-fingerprint"
          type="text"
          placeholder="SHA256:xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
          value={state.expectedFingerprint}
          onChange={(e) =>
            onChange({ ...state, expectedFingerprint: e.target.value })
          }
          className="font-mono text-xs"
          data-testid="check-expected-fingerprint-input"
        />
        <p className="text-xs text-muted-foreground">
          {t("network.fingerprintHelpPrefix")}{" "}
          <span className="font-medium text-foreground">
            {t("network.tunnelBastionLabel")}
          </span>{" "}
          {t("network.fingerprintHelpSuffix")}{" "}
          <code className="text-[11px]">
            ssh-keyscan host | ssh-keygen -lf -
          </code>
          .
        </p>
      </div>
      <div className="space-y-2">
        <Label htmlFor="username">{t("form.usernameOptional")}</Label>
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
        <Label htmlFor="password">{t("form.passwordOptional")}</Label>
        <Input
          id="password"
          type="password"
          value={state.password}
          onChange={(e) => onChange({ ...state, password: e.target.value })}
          data-testid="check-password-input"
        />
      </div>
      <div className="space-y-2">
        <Label htmlFor="private-key">
          {t("network.privateKeyOptionalPem")}
        </Label>
        <Textarea
          id="private-key"
          rows={6}
          value={state.privateKey}
          onChange={(e) => onChange({ ...state, privateKey: e.target.value })}
          placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
          className="font-mono text-xs"
          data-testid="check-private-key-input"
        />
        {hasStoredKey && !state.privateKey && (
          <p
            className="text-xs text-muted-foreground"
            data-testid="private-key-encrypted"
          >
            <span className="font-mono tracking-widest">••••</span>{" "}
            <span className="italic">{t("network.privateKeyEncrypted")}</span>
          </p>
        )}
        <p className="text-xs text-muted-foreground">
          {t("network.passwordOverridesPrivateKey")}
        </p>
      </div>
    </>
  );
}

// ── SFTP ──
// SFTP spells the pin `host_key_fingerprint`, not SSH's `expected_fingerprint`:
// the SSH key has a second job (it gates using the check as a tunnel bastion)
// that SFTP has no equivalent for, so the two stay separate keys. The shared
// state field is reused because the value is the same `SHA256:…` string.
export const sftpModule: CheckTypeModule<HostPortUserPassState> = {
  types: ["sftp"],
  ownedKeys: [
    "host",
    "port",
    "username",
    "password",
    "private_key",
    "expected_fingerprint",
    "host_key_fingerprint",
  ],
  fromConfig: (config) => ({
    ...hostPortUserPassFromConfig(config),
    expectedFingerprint: getConfigField(config, "host_key_fingerprint"),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.username) cfg.username = state.username;
    if (state.password) cfg.password = state.password;
    if (state.privateKey) cfg.private_key = state.privateKey;
    if (state.expectedFingerprint) {
      cfg.host_key_fingerprint = state.expectedFingerprint;
    }
    return { config: cfg, errors: hostRequired(state.host) };
  },
  Fields: SftpFields,
};

function SftpFields({
  state,
  onChange,
}: CheckTypeFieldsProps<HostPortUserPassState>) {
  const { t } = useTranslation("checks");
  const { configPrivateKeys } = useCheckFormFields();
  const hasStoredKey = configPrivateKeys?.includes("private_key") ?? false;
  return (
    <>
      <div className="space-y-2">
        <Label>{t("form.host")}</Label>
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
        <Label htmlFor="username">{t("form.username")}</Label>
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
        <Label htmlFor="password">{t("network.password")}</Label>
        <Input
          id="password"
          type="password"
          value={state.password}
          onChange={(e) => onChange({ ...state, password: e.target.value })}
          data-testid="check-password-input"
        />
      </div>
      <div className="space-y-2">
        <Label htmlFor="private-key">
          {t("network.privateKeyOptionalPem")}
        </Label>
        <Textarea
          id="private-key"
          rows={6}
          value={state.privateKey}
          onChange={(e) => onChange({ ...state, privateKey: e.target.value })}
          placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
          className="font-mono text-xs"
          data-testid="check-private-key-input"
        />
        {hasStoredKey && !state.privateKey && (
          <p
            className="text-xs text-muted-foreground"
            data-testid="private-key-encrypted"
          >
            <span className="font-mono tracking-widest">••••</span>{" "}
            <span className="italic">{t("network.privateKeyEncrypted")}</span>
          </p>
        )}
        <p className="text-xs text-muted-foreground">
          {t("network.passwordOverridesPrivateKey")}
        </p>
      </div>
      <div className="space-y-2">
        <Label htmlFor="host-key-fingerprint">
          {t("network.hostKeyFingerprintOptional")}
        </Label>
        <Input
          id="host-key-fingerprint"
          type="text"
          placeholder="SHA256:xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
          value={state.expectedFingerprint}
          onChange={(e) =>
            onChange({ ...state, expectedFingerprint: e.target.value })
          }
          className="font-mono text-xs"
          data-testid="check-host-key-fingerprint-input"
        />
        <p className="text-xs text-muted-foreground">
          {t("network.sftpFingerprintHelp")}
        </p>
      </div>
    </>
  );
}

// ── FTP ──
export const ftpModule: CheckTypeModule<HostPortUserPassState> = {
  types: ["ftp"],
  ownedKeys: [
    "host",
    "port",
    "username",
    "password",
    "private_key",
    "expected_fingerprint",
  ],
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
  const { t } = useTranslation("checks");
  return (
    <>
      <div className="space-y-2">
        <Label>{t("form.host")}</Label>
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
        <Label htmlFor="username">
          {t("network.usernameOptionalAnonymous")}
        </Label>
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
        <Label htmlFor="password">{t("form.passwordOptional")}</Label>
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
  ownedKeys: ["host"],
  fromConfig: (config) => ({ host: getConfigField(config, "host") }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    return { config: cfg, errors: hostRequired(state.host) };
  },
  Fields: IcmpFields,
};

function IcmpFields({
  state,
  onChange,
  errors,
}: CheckTypeFieldsProps<IcmpState>) {
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
        <Label htmlFor="host">{t("form.host")}</Label>
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
