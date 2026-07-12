import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Checkbox } from "@/components/ui/checkbox";
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
  const isMysql = type === "mysql";
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
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
        <Label htmlFor="username">Username</Label>
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
          placeholder={isMysql ? "mysql" : "postgres"}
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
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
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
        <Label htmlFor="database">Database (optional, 0-15)</Label>
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
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
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
        <Label htmlFor="username">Username (optional)</Label>
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
}

export const rabbitmqModule: CheckTypeModule<RabbitmqState> = {
  types: ["rabbitmq"],
  fromConfig: (config) => ({
    host: getConfigField(config, "host"),
    port: getConfigField(config, "port"),
    username: getConfigField(config, "username"),
    password: getConfigField(config, "password"),
    vhost: getConfigField(config, "vhost"),
    queue: getConfigField(config, "queue"),
    // Preserved quirk: the "Use TLS" checkbox was seeded from the shared
    // `tls_verify` field (not `tls`), so an existing rabbitmq check's `tls`
    // flag does not re-check the box on edit unless the user toggles it.
    tls: getConfigField(config, "tls_verify") === "true",
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.port) cfg.port = parseInt(state.port, 10);
    if (state.username) cfg.username = state.username;
    if (state.password) cfg.password = state.password;
    if (state.vhost) cfg.vhost = state.vhost;
    if (state.queue) cfg.queue = state.queue;
    if (state.tls) cfg.tls = true;
    return { config: cfg, errors: hostRequired(state.host) };
  },
  Fields: RabbitmqFields,
};

function RabbitmqFields({ state, onChange }: CheckTypeFieldsProps<RabbitmqState>) {
  return (
    <>
      <div className="space-y-2">
        <Label>Host</Label>
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
          <Input
            id="port"
            type="number"
            placeholder="5672"
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
          placeholder="guest"
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
        <Label htmlFor="vhost">Virtual Host (optional)</Label>
        <Input
          id="vhost"
          type="text"
          placeholder="/"
          value={state.vhost}
          onChange={(e) => onChange({ ...state, vhost: e.target.value })}
          data-testid="check-vhost-input"
        />
      </div>
      <div className="space-y-2">
        <Label htmlFor="queue">Queue (optional)</Label>
        <Input
          id="queue"
          type="text"
          placeholder="my-queue"
          value={state.queue}
          onChange={(e) => onChange({ ...state, queue: e.target.value })}
          data-testid="check-queue-input"
        />
      </div>
      <div className="space-y-3">
        <label className="flex items-center gap-2">
          <Checkbox
            checked={state.tls}
            onCheckedChange={(v) => onChange({ ...state, tls: v === true })}
            data-testid="check-tls-checkbox"
          />
          <span className="text-sm">Use TLS</span>
        </label>
      </div>
    </>
  );
}
