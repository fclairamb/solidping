import { cn } from "@/lib/utils";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Checkbox } from "@/components/ui/checkbox";
import { getFieldError } from "@/hooks/use-check-validation";
import type { CheckTypeModule } from "./index";
import type { CheckConfig, CheckTypeFieldsProps, FieldErrors } from "./common";
import { getConfigField } from "./common";

const hostRequired = (host: string): FieldErrors =>
  host ? [] : [{ name: "host", message: "Host is required" }];

// ── gRPC ──
export interface GrpcState {
  host: string;
  port: string;
  serviceName: string;
  tls: boolean;
}

export const grpcModule: CheckTypeModule<GrpcState> = {
  types: ["grpc"],
  fromConfig: (config) => ({
    host: getConfigField(config, "host"),
    port: getConfigField(config, "port"),
    serviceName: getConfigField(config, "serviceName"),
    tls: getConfigField(config, "tls") === "true",
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.serviceName) cfg.serviceName = state.serviceName;
    if (state.tls) cfg.tls = true;
    return { config: cfg, errors: hostRequired(state.host) };
  },
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
          onCheckedChange={(v) => onChange({ ...state, tls: v === true })}
        />
        <span className="text-sm">Use TLS</span>
      </label>
    </>
  );
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
