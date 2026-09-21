import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
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
  // Burst settings (spec 2026-09-21-02). All strings, same display-state
  // pattern as `port` above: `count`/`packetSize`/`ttl` parse to integers in
  // `toConfig`, `interval` stays the raw Go duration string the checker
  // stores ("100ms", "1s" — see `checkicmp.ICMPConfig.FromMap`). Empty means
  // "leave the server default in place", so an untouched burst is a single
  // ping exactly as before.
  count: string;
  interval: string;
  packetSize: string;
  ttl: string;
}

// parsePositiveInt turns a display string into a non-negative integer, or
// null when the input is blank/not a whole number (the number input's e.g.
// "1e" collapses to ""). 0 is VALID here — packet_size 0 passes the server
// validator — so 0 flows through and only blank/garbage is treated as unset.
const parseNonNegativeInt = (raw: string): number | null => {
  if (!raw.trim()) return null;
  const n = Number(raw);
  if (!Number.isInteger(n) || n < 0) return null;
  return n;
};

// intervalFormatRE is a loose Go-duration shape check, NOT a bounds check: it
// only catches obvious typos ("100", "s100", "1sec") before the request.
// The server (`server/internal/checkers/checkicmp/checker.go`, which owns
// minCount/maxCount/minInterval/maxInterval and the packet_size/ttl ranges —
// the single source of these limits, NOT this file) still validates the
// value and its bounds.
const intervalFormatRE = /^(?:\d+(?:\.\d+)?(?:ms|s|m|h))+$/;

// icmpBurstSummary builds the one-line "what this burst does" explainer, live
// from the entered values. Count ≤ 1 (or blank) is the default single ping;
// count ≥ 2 names the spacing, falling back to the server's 1s default. It
// takes `t` because the wording is localized (en/fr/de/es) and plural is
// picked in code — there is no i18next plural-rules config to lean on.
export const icmpBurstSummary = (
  t: TFunction,
  count: string,
  interval: string,
): string => {
  const n = parseNonNegativeInt(count) ?? 0;
  if (n <= 1) return t("network.burstOne");
  return t("network.burstMany", {
    count: n,
    interval: interval.trim() || "1s",
  });
};

const burstCustomized = (state: IcmpState): boolean =>
  Boolean(
    state.count.trim() ||
      state.interval.trim() ||
      state.packetSize.trim() ||
      state.ttl.trim(),
  );

export const icmpModule: CheckTypeModule<IcmpState> = {
  types: ["icmp"],
  // Owned = omit-to-clear (see `assembleSubmittedConfig`). `host` was always
  // here; `count`/`interval`/`packet_size`/`ttl` joined so clearing one of
  // the new inputs really deletes the stored key instead of the unmodeled
  // passthrough silently re-sending it. `timeout` and `ipVersion` stay with
  // the shared controls (SHARED_FORM_CONFIG_KEYS); `url` stays unmodeled.
  ownedKeys: ["host", "count", "interval", "packet_size", "ttl"],
  fromConfig: (config) => ({
    host: getConfigField(config, "host"),
    count: getConfigField(config, "count"),
    interval: getConfigField(config, "interval"),
    packetSize: getConfigField(config, "packet_size"),
    ttl: getConfigField(config, "ttl"),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;

    // The module checks only shape (integer / duration format); the numeric
    // bounds live in the Go checker and nowhere else (see intervalFormatRE).
    const count = parseNonNegativeInt(state.count);
    if (state.count.trim() && count === null) {
      return {
        config: cfg,
        errors: [
          { name: "count", message: "Count must be a whole number of packets" },
          ...hostRequired(state.host),
        ],
      };
    }
    if (count !== null && count > 0) cfg.count = count;

    // Interval is only meaningful for a burst of 2+ packets: at the default
    // single ping it is dead config (the checker never uses it), so it is
    // omitted — which, owned-keys being omit-to-clear, also deletes a stored
    // interval the moment the count drops back to 1. The input itself stays
    // visible but disabled so the value is never silently hidden.
    const effectiveCount = count ?? 1;
    if (state.interval.trim() && effectiveCount > 1) {
      if (!intervalFormatRE.test(state.interval.trim())) {
        return {
          config: cfg,
          errors: [
            {
              name: "interval",
              message: "Interval must be a duration like 100ms or 1s",
            },
            ...hostRequired(state.host),
          ],
        };
      }
      cfg.interval = state.interval.trim();
    }

    const packetSize = parseNonNegativeInt(state.packetSize);
    if (state.packetSize.trim() && packetSize === null) {
      return {
        config: cfg,
        errors: [
          {
            name: "packet_size",
            message: "Packet size must be a whole number of bytes",
          },
          ...hostRequired(state.host),
        ],
      };
    }
    if (packetSize !== null) cfg.packet_size = packetSize;

    const ttl = parseNonNegativeInt(state.ttl);
    if (state.ttl.trim() && ttl === null) {
      return {
        config: cfg,
        errors: [
          { name: "ttl", message: "TTL must be a whole number" },
          ...hostRequired(state.host),
        ],
      };
    }
    if (ttl !== null && ttl > 0) cfg.ttl = ttl;

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
  // Interval only means something once the burst has ≥ 2 packets.
  const count = parseNonNegativeInt(state.count) ?? 1;
  const intervalEnabled = count > 1;
  const customized = burstCustomized(state);
  const burstLine = icmpBurstSummary(t, state.count, state.interval);
  return (
    <div className="space-y-4">
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
      <CollapsibleSection
        title={t("network.burstTitle")}
        summary={burstLine}
        customized={customized}
        defaultOpen={customized}
        data-testid="check-icmp-burst-section"
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-2">
            <Label htmlFor="icmp-count">{t("network.burstCount")}</Label>
            <Input
              id="icmp-count"
              type="number"
              min={1}
              step={1}
              placeholder="1"
              value={state.count}
              onChange={(e) => onChange({ ...state, count: e.target.value })}
              className={cn(
                getFieldError(errors, "count") && "border-destructive",
              )}
              data-testid="check-icmp-count-input"
            />
            {getFieldError(errors, "count") && (
              <p className="text-xs text-destructive">
                {getFieldError(errors, "count")}
              </p>
            )}
          </div>
          <div className="space-y-2">
            <Label htmlFor="icmp-interval">
              {t("network.burstInterval")}
            </Label>
            <Input
              id="icmp-interval"
              type="text"
              inputMode="decimal"
              placeholder="1s"
              disabled={!intervalEnabled}
              value={state.interval}
              onChange={(e) =>
                onChange({ ...state, interval: e.target.value })
              }
              className={cn(
                getFieldError(errors, "interval") && "border-destructive",
              )}
              data-testid="check-icmp-interval-input"
            />
            {getFieldError(errors, "interval") ? (
              <p className="text-xs text-destructive">
                {getFieldError(errors, "interval")}
              </p>
            ) : (
              !intervalEnabled && (
                <p className="text-xs text-muted-foreground">
                  {t("network.burstIntervalNeedsCount")}
                </p>
              )
            )}
          </div>
          <div className="space-y-2">
            <Label htmlFor="icmp-packet-size">
              {t("network.burstPacketSize")}
            </Label>
            <Input
              id="icmp-packet-size"
              type="number"
              min={0}
              step={1}
              placeholder="56"
              value={state.packetSize}
              onChange={(e) =>
                onChange({ ...state, packetSize: e.target.value })
              }
              className={cn(
                getFieldError(errors, "packet_size") && "border-destructive",
              )}
              data-testid="check-icmp-packet-size-input"
            />
            {getFieldError(errors, "packet_size") && (
              <p className="text-xs text-destructive">
                {getFieldError(errors, "packet_size")}
              </p>
            )}
          </div>
          <div className="space-y-2">
            <Label htmlFor="icmp-ttl">{t("network.burstTtl")}</Label>
            <Input
              id="icmp-ttl"
              type="number"
              min={1}
              max={255}
              step={1}
              placeholder="64"
              value={state.ttl}
              onChange={(e) => onChange({ ...state, ttl: e.target.value })}
              className={cn(
                getFieldError(errors, "ttl") && "border-destructive",
              )}
              data-testid="check-icmp-ttl-input"
            />
            {getFieldError(errors, "ttl") && (
              <p className="text-xs text-destructive">
                {getFieldError(errors, "ttl")}
              </p>
            )}
          </div>
        </div>
        <p className="text-xs text-muted-foreground" data-testid="icmp-burst-line">
          {burstLine}
        </p>
      </CollapsibleSection>
    </div>
  );
}
