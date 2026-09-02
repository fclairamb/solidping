import { useTranslation } from "react-i18next";
import { Plus, Trash2 } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Checkbox } from "@/components/ui/checkbox";
import { Switch } from "@/components/ui/switch";
import { getFieldError } from "@/hooks/use-check-validation";
import type { CheckTypeModule } from "./index";
import type { CheckConfig, CheckTypeFieldsProps, FieldErrors } from "./common";
import { getConfigField } from "./common";
import { useCheckFormFields } from "./context";

const hostRequired = (host: string): FieldErrors =>
  host ? [] : [{ name: "host", message: "Host is required" }];

// ── gRPC ──

/** One editable metadata row. */
export interface MetadataRow {
  key: string;
  value: string;
}

export interface GrpcState {
  host: string;
  port: string;
  serviceName: string;
  tls: boolean;
  // Only meaningful with TLS on, and only ever written to config when TLS is
  // on — the same omit-at-default style the server's GetConfig uses.
  tlsSkipVerify: boolean;
  // Plain, queryable request metadata (the gRPC analog of HTTP `headers`).
  // A public config key, so it always comes back on GET and needs no dirty
  // flag: omitting it is itself what clears a stored value.
  metadata: MetadataRow[];
  // Encrypted at rest and never returned by GET, so — exactly like the HTTP
  // form's secret headers — an untouched section MUST NOT serialize the key,
  // or every unrelated save would wipe the stored values.
  secretMetadata: MetadataRow[];
  secretMetadataDirty: boolean;
}

// GRPC_METADATA_KEY_RE mirrors the server's rule (config.go
// `metadataKeyPattern`): gRPC lowercases metadata keys on the wire, so an
// uppercase key is a silent rename and is rejected rather than folded.
const GRPC_METADATA_KEY_RE = /^[a-z0-9._-]+$/;

/** Reports whether a metadata key would be rejected by the server. */
export function isValidGrpcMetadataKey(key: string): boolean {
  return (
    GRPC_METADATA_KEY_RE.test(key) &&
    !key.startsWith("grpc-") &&
    !key.endsWith("-bin")
  );
}

function rowsFromConfig(config: CheckConfig, field: string): MetadataRow[] {
  const raw = config[field];
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return [];
  return Object.entries(raw as Record<string, string>).map(([key, value]) => ({
    key,
    value: String(value),
  }));
}

function rowsToMap(rows: MetadataRow[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const { key, value } of rows) {
    if (key) out[key] = value;
  }
  return out;
}

function grpcFromConfig(config: CheckConfig): GrpcState {
  const secretMetadata = rowsFromConfig(config, "secretMetadata");
  return {
    host: getConfigField(config, "host"),
    port: getConfigField(config, "port"),
    serviceName: getConfigField(config, "serviceName"),
    tls: getConfigField(config, "tls") === "true",
    tlsSkipVerify: getConfigField(config, "tlsSkipVerify") === "true",
    metadata: rowsFromConfig(config, "metadata"),
    secretMetadata,
    // Seeded from whether the config actually carried values — on a deployment
    // running the plaintext fallback they do come back, and they must then
    // round-trip rather than be dropped.
    secretMetadataDirty: secretMetadata.length > 0,
  };
}

function grpcToConfig(state: GrpcState): {
  config: CheckConfig;
  errors: FieldErrors;
} {
  const cfg: CheckConfig = {};
  if (state.host) cfg.host = state.host;
  if (state.port) cfg.port = parseInt(state.port, 10);
  if (state.serviceName) cfg.serviceName = state.serviceName;
  if (state.tls) cfg.tls = true;
  // Only meaningful under TLS: writing it for an h2c check would leave a
  // scary-looking key on a config where it does nothing.
  if (state.tls && state.tlsSkipVerify) cfg.tlsSkipVerify = true;

  const metadata = rowsToMap(state.metadata);
  if (Object.keys(metadata).length > 0) cfg.metadata = metadata;

  if (state.secretMetadataDirty) {
    // An explicit {} clears; when the section is untouched the key is absent
    // entirely and the server's preserve-absent-secrets merge keeps the
    // stored values.
    cfg.secretMetadata = rowsToMap(state.secretMetadata);
  }

  const errors = hostRequired(state.host);
  const invalidKeys = [...state.metadata, ...state.secretMetadata]
    .map((row) => row.key)
    .filter((key) => key && !isValidGrpcMetadataKey(key));
  if (invalidKeys.length > 0) {
    errors.push({
      name: "metadata",
      message: `Invalid metadata key${invalidKeys.length > 1 ? "s" : ""}: ${invalidKeys.join(", ")} (lowercase letters, digits, '-', '.' or '_'; 'grpc-' and '-bin' are reserved)`,
    });
  }
  return { config: cfg, errors };
}

export const grpcModule: CheckTypeModule<GrpcState> = {
  types: ["grpc"],
  fromConfig: grpcFromConfig,
  toConfig: grpcToConfig,
  Fields: GrpcFields,
};

function GrpcFields({ state, onChange }: CheckTypeFieldsProps<GrpcState>) {
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
        <div className="flex gap-2">
          <Input
            id="host"
            type="text"
            placeholder="grpc.example.com"
            value={state.host}
            onChange={(e) => onChange({ ...state, host: e.target.value })}
            className="flex-1"
          />
          <Input
            id="port"
            type="number"
            placeholder="50051"
            value={state.port}
            onChange={(e) => onChange({ ...state, port: e.target.value })}
            className="w-24"
          />
        </div>
      </div>
      <div className="space-y-2">
        <Label htmlFor="serviceName">Service Name (optional)</Label>
        <Input
          id="serviceName"
          type="text"
          placeholder="myservice"
          value={state.serviceName}
          onChange={(e) => onChange({ ...state, serviceName: e.target.value })}
        />
        <p className="text-xs text-muted-foreground">
          Leave empty to check overall server health
        </p>
      </div>
      <label className="flex items-center gap-2">
        <Checkbox
          checked={state.tls}
          data-testid="check-grpc-tls-checkbox"
          onCheckedChange={(v) => onChange({ ...state, tls: v === true })}
        />
        <span className="text-sm">Use TLS</span>
      </label>
    </>
  );
}

// GrpcMetadataEditor is the shared key/value editor behind both the plain and
// the secret metadata sections. `secret` masks the values and is what makes the
// section report itself dirty — the plain map is a public config key that always
// comes back on GET, so it needs no such guard.
function GrpcMetadataEditor({
  rows,
  onRowsChange,
  secret,
  testIdPrefix,
  addLabel,
}: {
  rows: MetadataRow[];
  onRowsChange: (next: MetadataRow[]) => void;
  secret: boolean;
  testIdPrefix: string;
  addLabel: string;
}) {
  const { t } = useTranslation("checks");
  return (
    <>
      {rows.map((row, idx) => (
        <div key={idx} className="flex gap-2 items-center">
          <Input
            type="text"
            placeholder={t("grpc.metadataKeyPlaceholder")}
            value={row.key}
            onChange={(e) => {
              const updated = [...rows];
              updated[idx] = { ...updated[idx], key: e.target.value };
              onRowsChange(updated);
            }}
            className={cn(
              "flex-1",
              row.key &&
                !isValidGrpcMetadataKey(row.key) &&
                "border-destructive",
            )}
            data-testid={`${testIdPrefix}-key-${idx}`}
          />
          <Input
            type={secret ? "password" : "text"}
            placeholder={t("grpc.metadataValuePlaceholder")}
            value={row.value}
            onChange={(e) => {
              const updated = [...rows];
              updated[idx] = { ...updated[idx], value: e.target.value };
              onRowsChange(updated);
            }}
            className="flex-1"
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
        data-testid={`${testIdPrefix}-add-button`}
      >
        <Plus className="h-4 w-4 mr-1" />
        {addLabel}
      </Button>
    </>
  );
}

// GrpcAdvancedFields renders the gRPC-specific half of the "Advanced" section:
// the TLS verification toggle and the plain request metadata. The per-check
// timeout is NOT duplicated here — check-form already layers a shared,
// protocol-agnostic `timeout` onto the config for every non-passive type.
export function GrpcAdvancedFields({
  state,
  onChange,
  errors,
}: CheckTypeFieldsProps<GrpcState>) {
  const { t } = useTranslation("checks");
  const metadataError = getFieldError(errors, "metadata");
  return (
    <div className="space-y-3">
      {state.tls && (
        <>
          <div className="flex items-center gap-2">
            <Switch
              id="grpc-tls-skip-verify"
              checked={state.tlsSkipVerify}
              onCheckedChange={(tlsSkipVerify) =>
                onChange({ ...state, tlsSkipVerify })
              }
              data-testid="check-grpc-tls-skip-verify-switch"
            />
            <Label htmlFor="grpc-tls-skip-verify">
              {t("grpc.tlsSkipVerify")}
            </Label>
          </div>
          {state.tlsSkipVerify && (
            <p
              className="text-xs text-yellow-700 dark:text-yellow-400"
              data-testid="check-grpc-tls-skip-verify-warning"
            >
              {t("grpc.tlsSkipVerifyWarning")}
            </p>
          )}
        </>
      )}
      <div className="space-y-2 border-t pt-3">
        <div>
          <Label>{t("grpc.metadata")}</Label>
          <p className="text-xs text-muted-foreground mt-0.5">
            {t("grpc.metadataDescription")}
          </p>
        </div>
        <GrpcMetadataEditor
          rows={state.metadata}
          onRowsChange={(metadata) => onChange({ ...state, metadata })}
          secret={false}
          testIdPrefix="grpc-metadata"
          addLabel={t("grpc.addMetadata")}
        />
        {metadataError && (
          <p className="text-xs text-destructive" data-testid="grpc-metadata-error">
            {metadataError}
          </p>
        )}
      </div>
    </div>
  );
}

// grpcAdvancedSummary drives the "Advanced" section's collapsed summary line.
export function grpcAdvancedSummary(state: GrpcState): {
  text: string;
  customized: boolean;
} {
  const parts: string[] = [];
  if (state.tls && state.tlsSkipVerify) parts.push("TLS verification off");
  const count = state.metadata.filter((row) => row.key).length;
  if (count > 0) parts.push(`${count} metadata entr${count === 1 ? "y" : "ies"}`);
  return { text: parts.join(" · "), customized: parts.length > 0 };
}

// GrpcAuthFields renders the "Authentication & secrets" section: request
// metadata whose values are encrypted at rest (an `authorization` bearer, an
// `x-api-key`) so a health endpoint behind an authenticating proxy can be
// checked at all.
export function GrpcAuthFields({
  state,
  onChange,
}: CheckTypeFieldsProps<GrpcState>) {
  const { t } = useTranslation("checks");
  const { configPrivateKeys } = useCheckFormFields();
  return (
    <div className="space-y-2">
      <div>
        <Label>{t("grpc.secretMetadata")}</Label>
        <p className="text-xs text-muted-foreground mt-0.5">
          {t("grpc.secretMetadataDescription")}
        </p>
      </div>
      {configPrivateKeys?.includes("secretMetadata") &&
        !state.secretMetadataDirty && (
          <p
            className="text-xs text-muted-foreground"
            data-testid="grpc-secret-metadata-encrypted"
          >
            <span className="font-mono tracking-widest">••••</span>{" "}
            <span className="italic">{t("grpc.secretMetadataEncrypted")}</span>
          </p>
        )}
      <GrpcMetadataEditor
        rows={state.secretMetadata}
        onRowsChange={(secretMetadata) =>
          // Editing a row is what dirties the section; merely adding a blank
          // one is not — otherwise a stray click on "add" followed by a save
          // would clear the stored metadata.
          onChange({
            ...state,
            secretMetadata,
            secretMetadataDirty:
              state.secretMetadataDirty ||
              secretMetadata.some((row) => row.key || row.value) ||
              secretMetadata.length < state.secretMetadata.length,
          })
        }
        secret
        testIdPrefix="grpc-secret-metadata"
        addLabel={t("grpc.addSecretMetadata")}
      />
    </div>
  );
}

// grpcAuthSummary accounts for secret metadata that is stored encrypted and
// therefore absent from the form state — `configPrivateKeys` is the only
// evidence the form has that the check carries any.
export function grpcAuthSummary(
  state: GrpcState,
  configPrivateKeys?: string[],
): { text: string; customized: boolean } {
  const count = state.secretMetadata.filter((row) => row.key).length;
  if (count > 0) {
    return {
      text: `${count} secret metadata entr${count === 1 ? "y" : "ies"}`,
      customized: true,
    };
  }
  if (
    !state.secretMetadataDirty &&
    configPrivateKeys?.includes("secretMetadata")
  ) {
    return { text: "secret metadata", customized: true };
  }
  return { text: "none", customized: false };
}

// ── Kafka ──
export interface KafkaState {
  brokers: string;
  topic: string;
  username: string;
  password: string;
  tls: boolean;
  produceTest: boolean;
}

export const kafkaModule: CheckTypeModule<KafkaState> = {
  types: ["kafka"],
  fromConfig: (config) => ({
    brokers: Array.isArray(config.brokers)
      ? (config.brokers as string[]).join(", ")
      : getConfigField(config, "brokers"),
    topic: getConfigField(config, "topic"),
    username: getConfigField(config, "username"),
    password: getConfigField(config, "password"),
    tls: getConfigField(config, "tls") === "true",
    produceTest: getConfigField(config, "produceTest") === "true",
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.brokers)
      cfg.brokers = state.brokers
        .split(",")
        .map((b) => b.trim())
        .filter(Boolean);
    if (state.topic) cfg.topic = state.topic;
    if (state.username) cfg.saslUsername = state.username;
    if (state.password) cfg.saslPassword = state.password;
    if (state.username || state.password) cfg.saslMechanism = "PLAIN";
    if (state.tls) cfg.tls = true;
    if (state.produceTest) cfg.produceTest = true;
    const errors: FieldErrors = state.brokers
      ? []
      : [{ name: "brokers", message: "Brokers are required" }];
    return { config: cfg, errors };
  },
  Fields: KafkaFields,
};

function KafkaFields({ state, onChange }: CheckTypeFieldsProps<KafkaState>) {
  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="brokers">Brokers</Label>
        <Input
          id="brokers"
          type="text"
          placeholder="broker1:9092, broker2:9092"
          value={state.brokers}
          onChange={(e) => onChange({ ...state, brokers: e.target.value })}
          data-testid="check-brokers-input"
        />
        <p className="text-xs text-muted-foreground">
          Comma-separated list of broker addresses (host:port)
        </p>
      </div>
      <div className="space-y-2">
        <Label htmlFor="topic">Topic (optional)</Label>
        <Input
          id="topic"
          type="text"
          placeholder="my-topic"
          value={state.topic}
          onChange={(e) => onChange({ ...state, topic: e.target.value })}
          data-testid="check-topic-input"
        />
      </div>
      <div className="flex gap-4">
        <div className="space-y-2 flex-1">
          <Label htmlFor="username">SASL Username (optional)</Label>
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
          <Label htmlFor="password">SASL Password (optional)</Label>
          <Input
            id="password"
            type="password"
            value={state.password}
            onChange={(e) => onChange({ ...state, password: e.target.value })}
            data-testid="check-password-input"
          />
        </div>
      </div>
      <div className="space-y-3">
        <label className="flex items-center gap-2">
          <Checkbox
            checked={state.tls}
            onCheckedChange={(v) => onChange({ ...state, tls: v === true })}
          />
          <span className="text-sm">Use TLS</span>
        </label>
        <label className="flex items-center gap-2">
          <Checkbox
            checked={state.produceTest}
            onCheckedChange={(v) =>
              onChange({ ...state, produceTest: v === true })
            }
          />
          <span className="text-sm">Test message production (requires topic)</span>
        </label>
      </div>
    </>
  );
}

// ── MQTT ──
export interface MqttState {
  host: string;
  port: string;
  username: string;
  password: string;
  topic: string;
  tls: boolean;
}

export const mqttModule: CheckTypeModule<MqttState> = {
  types: ["mqtt"],
  fromConfig: (config) => ({
    host: getConfigField(config, "host"),
    port: getConfigField(config, "port"),
    username: getConfigField(config, "username"),
    password: getConfigField(config, "password"),
    topic: getConfigField(config, "topic"),
    tls: getConfigField(config, "tls") === "true",
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.username) cfg.username = state.username;
    if (state.password) cfg.password = state.password;
    if (state.topic) cfg.topic = state.topic;
    if (state.tls) cfg.tls = true;
    return { config: cfg, errors: hostRequired(state.host) };
  },
  Fields: MqttFields,
};

function MqttFields({ state, onChange, errors }: CheckTypeFieldsProps<MqttState>) {
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
        <div className="flex gap-2">
          <Input
            id="host"
            type="text"
            placeholder="broker.example.com"
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
            placeholder="1883"
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
      <div className="flex gap-4">
        <div className="space-y-2 flex-1">
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
        <Label htmlFor="topic">Topic (optional)</Label>
        <Input
          id="topic"
          type="text"
          placeholder="solidping/healthcheck"
          value={state.topic}
          onChange={(e) => onChange({ ...state, topic: e.target.value })}
          className={cn(getFieldError(errors, "topic") && "border-destructive")}
          data-testid="check-topic-input"
        />
        {getFieldError(errors, "topic") && (
          <p className="text-xs text-destructive">
            {getFieldError(errors, "topic")}
          </p>
        )}
      </div>
      <div className="space-y-3">
        <label className="flex items-center gap-2">
          <Checkbox
            checked={state.tls}
            onCheckedChange={(v) => onChange({ ...state, tls: v === true })}
            data-testid="check-tls-checkbox"
          />
          <span className="text-sm">Use TLS (port defaults to 8883)</span>
        </label>
      </div>
    </>
  );
}
