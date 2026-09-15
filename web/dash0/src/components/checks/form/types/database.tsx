import { useTranslation } from "react-i18next";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { CheckTypeModule } from "./index";
import type { CheckConfig, CheckTypeFieldsProps, FieldErrors } from "./common";
import { getConfigField } from "./common";
import { useCheckFormFields } from "./context";

const hostRequired = (host: string): FieldErrors =>
  host ? [] : [{ name: "host", message: "Host is required" }];

// ── SQL databases: postgresql / mysql / mssql / oracle ──
export interface SqlDbState {
  host: string;
  port: string;
  username: string;
  password: string;
  database: string;
  query: string;
}

export const sqlDatabaseModule: CheckTypeModule<SqlDbState> = {
  types: ["postgresql", "mysql", "mssql", "oracle"],
  ownedKeys: ["host", "port", "username", "password", "database", "query"],
  fromConfig: (config) => ({
    host: getConfigField(config, "host"),
    port: getConfigField(config, "port"),
    username: getConfigField(config, "username"),
    password: getConfigField(config, "password"),
    database: getConfigField(config, "database"),
    query: getConfigField(config, "query"),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.username) cfg.username = state.username;
    if (state.password) cfg.password = state.password;
    if (state.database) cfg.database = state.database;
    if (state.query) cfg.query = state.query;
    return { config: cfg, errors: hostRequired(state.host) };
  },
  Fields: SqlDbFields,
};

function SqlDbFields({ state, onChange }: CheckTypeFieldsProps<SqlDbState>) {
  const { type } = useCheckFormFields();
  const { t } = useTranslation("checks");
  const isMysql = type === "mysql";
  return (
    <>
      <div className="space-y-2">
        <Label>{t("form.host")}</Label>
        <div className="flex gap-2">
          <Input
            id="host"
            type="text"
            placeholder="db.example.com"
            value={state.host}
            onChange={(e) => onChange({ ...state, host: e.target.value })}
            className="flex-1"
            data-testid="check-host-input"
          />
          <Input
            id="port"
            type="number"
            placeholder={isMysql ? "3306" : "5432"}
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
          placeholder={isMysql ? "root" : "postgres"}
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
        <Label htmlFor="database">{t("form.databaseOptional")}</Label>
        <Input
          id="database"
          type="text"
          placeholder={isMysql ? "mysql" : "postgres"}
          value={state.database}
          onChange={(e) => onChange({ ...state, database: e.target.value })}
          data-testid="check-database-input"
        />
      </div>
      <div className="space-y-2">
        <Label htmlFor="query">{t("form.queryOptional")}</Label>
        <Input
          id="query"
          type="text"
          placeholder="SELECT 1"
          value={state.query}
          onChange={(e) => onChange({ ...state, query: e.target.value })}
          data-testid="check-query-input"
        />
      </div>
    </>
  );
}

// ── Redis ──
export interface RedisState {
  host: string;
  port: string;
  password: string;
  database: string;
}

export const redisModule: CheckTypeModule<RedisState> = {
  types: ["redis"],
  ownedKeys: ["host", "port", "password", "database"],
  fromConfig: (config) => ({
    host: getConfigField(config, "host"),
    port: getConfigField(config, "port"),
    password: getConfigField(config, "password"),
    database: getConfigField(config, "database"),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.password) cfg.password = state.password;
    if (state.database) cfg.database = parseInt(state.database, 10);
    return { config: cfg, errors: hostRequired(state.host) };
  },
  Fields: RedisFields,
};

function RedisFields({ state, onChange }: CheckTypeFieldsProps<RedisState>) {
  const { t } = useTranslation("checks");
  return (
    <>
      <div className="space-y-2">
        <Label>{t("form.host")}</Label>
        <div className="flex gap-2">
          <Input
            id="host"
            type="text"
            placeholder="redis.example.com"
            value={state.host}
            onChange={(e) => onChange({ ...state, host: e.target.value })}
            className="flex-1"
            data-testid="check-host-input"
          />
          <Input
            id="port"
            type="number"
            placeholder="6379"
            value={state.port}
            onChange={(e) => onChange({ ...state, port: e.target.value })}
            className="w-24"
            data-testid="check-port-input"
          />
        </div>
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
        <Label htmlFor="database">{t("form.redisDatabaseOptional")}</Label>
        <Input
          id="database"
          type="number"
          placeholder="0"
          min={0}
          max={15}
          value={state.database}
          onChange={(e) => onChange({ ...state, database: e.target.value })}
          data-testid="check-database-input"
        />
      </div>
    </>
  );
}

// ── MongoDB ──
export interface MongoState {
  host: string;
  port: string;
  username: string;
  password: string;
  database: string;
}

export const mongodbModule: CheckTypeModule<MongoState> = {
  types: ["mongodb"],
  ownedKeys: ["host", "port", "username", "password", "database"],
  fromConfig: (config) => ({
    host: getConfigField(config, "host"),
    port: getConfigField(config, "port"),
    username: getConfigField(config, "username"),
    password: getConfigField(config, "password"),
    database: getConfigField(config, "database"),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.username) cfg.username = state.username;
    if (state.password) cfg.password = state.password;
    if (state.database) cfg.database = state.database;
    return { config: cfg, errors: hostRequired(state.host) };
  },
  Fields: MongoFields,
};

function MongoFields({ state, onChange }: CheckTypeFieldsProps<MongoState>) {
  const { t } = useTranslation("checks");
  return (
    <>
      <div className="space-y-2">
        <Label>{t("form.host")}</Label>
        <div className="flex gap-2">
          <Input
            id="host"
            type="text"
            placeholder="mongo.example.com"
            value={state.host}
            onChange={(e) => onChange({ ...state, host: e.target.value })}
            className="flex-1"
            data-testid="check-host-input"
          />
          <Input
            id="port"
            type="number"
            placeholder="27017"
            value={state.port}
            onChange={(e) => onChange({ ...state, port: e.target.value })}
            className="w-24"
            data-testid="check-port-input"
          />
        </div>
      </div>
      <div className="space-y-2">
        <Label htmlFor="username">{t("form.usernameOptional")}</Label>
        <Input
          id="username"
          type="text"
          placeholder="admin"
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
        <Label htmlFor="database">{t("form.databaseOptional")}</Label>
        <Input
          id="database"
          type="text"
          placeholder="admin"
          value={state.database}
          onChange={(e) => onChange({ ...state, database: e.target.value })}
          data-testid="check-database-input"
        />
      </div>
    </>
  );
}

// ── RabbitMQ ──
export interface RabbitmqState {
  host: string;
  port: string;
  username: string;
  password: string;
  vhost: string;
  queue: string;
  tls: boolean;
  mode: string;
  managementPort: string;
  memoryUsedWarning: string;
  memoryUsedCritical: string;
  diskFreeWarning: string;
  diskFreeCritical: string;
}

export const rabbitmqModule: CheckTypeModule<RabbitmqState> = {
  types: ["rabbitmq"],
  ownedKeys: [
    "host",
    "port",
    "username",
    "password",
    "vhost",
    "queue",
    "tls",
    "tls_verify",
    "mode",
    "managementPort",
    "memoryUsedWarning",
    "memoryUsedCritical",
    "diskFreeWarning",
    "diskFreeCritical",
  ],
  fromConfig: (config) => ({
    host: getConfigField(config, "host"),
    port: getConfigField(config, "port"),
    username: getConfigField(config, "username"),
    password: getConfigField(config, "password"),
    vhost: getConfigField(config, "vhost"),
    queue: getConfigField(config, "queue"),
    // The stored key is `tls` (RabbitMQConfig.TLS); `tls_verify` is only a
    // legacy spelling this form used to seed from. Reading `tls` FIRST matters:
    // seeding from `tls_verify` alone left the box unchecked for a TLS-enabled
    // check, and since `tls` is a key this module owns, the next save dropped
    // it — silently turning TLS off.
    tls:
      getConfigField(config, "tls") === "true" ||
      getConfigField(config, "tls_verify") === "true",
    mode: getConfigField(config, "mode") || "amqp",
    managementPort: getConfigField(config, "managementPort"),
    memoryUsedWarning: getConfigField(config, "memoryUsedWarning"),
    memoryUsedCritical: getConfigField(config, "memoryUsedCritical"),
    diskFreeWarning: getConfigField(config, "diskFreeWarning"),
    diskFreeCritical: getConfigField(config, "diskFreeCritical"),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.username) cfg.username = state.username;
    if (state.password) cfg.password = state.password;
    if (state.tls) cfg.tls = true;
    if (state.mode && state.mode !== "amqp") cfg.mode = state.mode;

    if (state.mode === "management") {
      if (state.managementPort) cfg.managementPort = parseInt(state.managementPort, 10);
      if (state.memoryUsedWarning) cfg.memoryUsedWarning = state.memoryUsedWarning;
      if (state.memoryUsedCritical) cfg.memoryUsedCritical = state.memoryUsedCritical;
      if (state.diskFreeWarning) cfg.diskFreeWarning = state.diskFreeWarning;
      if (state.diskFreeCritical) cfg.diskFreeCritical = state.diskFreeCritical;
    } else {
      // Vhost and Queue are AMQP-only (the backend inspects a queue, scoped
      // to a vhost, over AMQP only) and are hidden from the form in
      // management mode; keep them out of a management-mode payload too.
      if (state.vhost) cfg.vhost = state.vhost;
      if (state.queue) cfg.queue = state.queue;
    }

    return { config: cfg, errors: hostRequired(state.host) };
  },
  Fields: RabbitmqFields,
};

function RabbitmqFields({ state, onChange }: CheckTypeFieldsProps<RabbitmqState>) {
  const { t } = useTranslation("checks");
  const isManagement = state.mode === "management";

  return (
    <>
      <div className="space-y-2">
        <Label>{t("form.host")}</Label>
        <div className="flex gap-2">
          <Input
            id="host"
            type="text"
            placeholder="rabbitmq.example.com"
            value={state.host}
            onChange={(e) => onChange({ ...state, host: e.target.value })}
            className="flex-1"
            data-testid="check-host-input"
          />
          {isManagement ? (
            <Input
              id="managementPort"
              type="number"
              placeholder="15672"
              value={state.managementPort}
              onChange={(e) => onChange({ ...state, managementPort: e.target.value })}
              className="w-24"
              data-testid="check-rabbitmq-management-port-input"
            />
          ) : (
            <Input
              id="port"
              type="number"
              placeholder="5672"
              value={state.port}
              onChange={(e) => onChange({ ...state, port: e.target.value })}
              className="w-24"
              data-testid="check-port-input"
            />
          )}
        </div>
      </div>
      <div className="space-y-2">
        <Label htmlFor="rabbitmqMode">{t("rabbitmq.mode")}</Label>
        <Select value={state.mode} onValueChange={(mode) => onChange({ ...state, mode })}>
          <SelectTrigger id="rabbitmqMode" data-testid="check-rabbitmq-mode-select">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="amqp">{t("rabbitmq.modeAmqp")}</SelectItem>
            <SelectItem value="management">{t("rabbitmq.modeManagement")}</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div className="space-y-2">
        <Label htmlFor="username">{t("form.username")}</Label>
        <Input
          id="username"
          type="text"
          placeholder="guest"
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
      {!isManagement && (
        <div className="space-y-2">
          <Label htmlFor="vhost">{t("form.virtualHostOptional")}</Label>
          <Input
            id="vhost"
            type="text"
            placeholder="/"
            value={state.vhost}
            onChange={(e) => onChange({ ...state, vhost: e.target.value })}
            data-testid="check-vhost-input"
          />
        </div>
      )}
      {!isManagement && (
        <div className="space-y-2">
          <Label htmlFor="queue">{t("form.queueOptional")}</Label>
          <Input
            id="queue"
            type="text"
            placeholder="my-queue"
            value={state.queue}
            onChange={(e) => onChange({ ...state, queue: e.target.value })}
            data-testid="check-queue-input"
          />
        </div>
      )}
      {isManagement && (
        <>
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label htmlFor="rabbitmqMemoryWarning">{t("rabbitmq.memoryWarning")}</Label>
              <Input
                id="rabbitmqMemoryWarning"
                type="text"
                placeholder="80% or 1.5GiB"
                value={state.memoryUsedWarning}
                onChange={(e) => onChange({ ...state, memoryUsedWarning: e.target.value })}
                data-testid="check-rabbitmq-memory-warning-input"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="rabbitmqMemoryCritical">{t("rabbitmq.memoryCritical")}</Label>
              <Input
                id="rabbitmqMemoryCritical"
                type="text"
                placeholder="90% or 1.8GiB"
                value={state.memoryUsedCritical}
                onChange={(e) => onChange({ ...state, memoryUsedCritical: e.target.value })}
                data-testid="check-rabbitmq-memory-critical-input"
              />
            </div>
          </div>
          <p className="text-xs text-muted-foreground">{t("rabbitmq.memoryThresholdHelp")}</p>
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-2">
              <Label htmlFor="rabbitmqDiskWarning">{t("rabbitmq.diskWarning")}</Label>
              <Input
                id="rabbitmqDiskWarning"
                type="text"
                placeholder="20GiB"
                value={state.diskFreeWarning}
                onChange={(e) => onChange({ ...state, diskFreeWarning: e.target.value })}
                data-testid="check-rabbitmq-disk-warning-input"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="rabbitmqDiskCritical">{t("rabbitmq.diskCritical")}</Label>
              <Input
                id="rabbitmqDiskCritical"
                type="text"
                placeholder="5GiB"
                value={state.diskFreeCritical}
                onChange={(e) => onChange({ ...state, diskFreeCritical: e.target.value })}
                data-testid="check-rabbitmq-disk-critical-input"
              />
            </div>
          </div>
          <p className="text-xs text-muted-foreground">{t("rabbitmq.diskThresholdHelp")}</p>
        </>
      )}
      <div className="space-y-3">
        <label className="flex items-center gap-2">
          <Checkbox
            checked={state.tls}
            onCheckedChange={(v) => onChange({ ...state, tls: v === true })}
            data-testid="check-tls-checkbox"
          />
          <span className="text-sm">{t("form.useTls")}</span>
        </label>
      </div>
    </>
  );
}
