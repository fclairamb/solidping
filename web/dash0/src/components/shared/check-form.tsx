import { useState, useMemo } from "react";
import { ArrowLeft, Loader2 } from "lucide-react";
import CodeMirror from "@uiw/react-codemirror";
import { javascript } from "@codemirror/lang-javascript";
import { useCheckValidation, getFieldError } from "@/hooks/use-check-validation";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Checkbox } from "@/components/ui/checkbox";
import { ApiError } from "@/api/client";
import type { Check, CheckGroup, RegionDefinition } from "@/api/hooks";

type CheckType = "http" | "tcp" | "icmp" | "dns" | "ssl" | "heartbeat" | "domain" | "smtp" | "udp" | "ssh" | "pop3" | "imap" | "websocket" | "postgresql" | "ftp" | "sftp" | "js" | "mqtt";

function defaultPeriod(type: CheckType): string {
  if (type === "domain") return "24:00:00";
  return type === "dns" || type === "ssl" || type === "smtp" || type === "pop3" || type === "imap" || type === "websocket" || type === "postgresql" || type === "ftp" || type === "sftp" || type === "js" || type === "mqtt" ? "01:00:00" : "00:01:00";
}

const checkTypes: { value: CheckType; label: string; description: string }[] = [
  { value: "http", label: "HTTP", description: "Monitor HTTP/HTTPS endpoints" },
  { value: "tcp", label: "TCP", description: "Check TCP port connectivity" },
  { value: "icmp", label: "ICMP", description: "Ping hosts using ICMP" },
  { value: "dns", label: "DNS", description: "Verify DNS resolution" },
  { value: "ssl", label: "SSL", description: "Check SSL certificate validity" },
  { value: "heartbeat", label: "Heartbeat", description: "Monitor via incoming pings" },
  { value: "domain", label: "Domain", description: "Monitor domain name expiration" },
  { value: "smtp", label: "SMTP", description: "Check SMTP server availability" },
  { value: "udp", label: "UDP", description: "Check UDP port reachability" },
  { value: "ssh", label: "SSH", description: "Check SSH server availability" },
  { value: "pop3", label: "POP3", description: "Check POP3 server availability" },
  { value: "imap", label: "IMAP", description: "Check IMAP server availability" },
  { value: "websocket", label: "WebSocket", description: "Check WebSocket connectivity" },
  { value: "postgresql", label: "PostgreSQL", description: "Check PostgreSQL database health" },
  { value: "ftp", label: "FTP", description: "Check FTP server availability" },
  { value: "sftp", label: "SFTP", description: "Check SFTP server availability" },
  { value: "js", label: "JavaScript", description: "Run custom JavaScript monitoring scripts" },
  { value: "mqtt", label: "MQTT", description: "Check MQTT broker connectivity" },
];

const intervalOptions = [
  { value: "00:00:10", label: "10 seconds" },
  { value: "00:00:30", label: "30 seconds" },
  { value: "00:01:00", label: "1 minute" },
  { value: "00:05:00", label: "5 minutes" },
  { value: "00:10:00", label: "10 minutes" },
  { value: "00:30:00", label: "30 minutes" },
  { value: "01:00:00", label: "1 hour" },
];

type PeriodUnit = "minutes" | "hours" | "days" | "weeks";

const periodUnits: { value: PeriodUnit; label: string }[] = [
  { value: "minutes", label: "Minutes" },
  { value: "hours", label: "Hours" },
  { value: "days", label: "Days" },
  { value: "weeks", label: "Weeks" },
];

function parsePeriod(period: string): { value: number; unit: PeriodUnit } {
  const [h, m, s] = period.split(":").map(Number);
  const totalSeconds = h * 3600 + m * 60 + s;
  if (totalSeconds % (7 * 86400) === 0) return { value: totalSeconds / (7 * 86400), unit: "weeks" };
  if (totalSeconds % 86400 === 0) return { value: totalSeconds / 86400, unit: "days" };
  if (totalSeconds % 3600 === 0) return { value: totalSeconds / 3600, unit: "hours" };
  return { value: Math.max(1, Math.round(totalSeconds / 60)), unit: "minutes" };
}

function formatPeriod(value: number, unit: PeriodUnit): string {
  const multipliers = { minutes: 60, hours: 3600, days: 86400, weeks: 604800 };
  const totalSeconds = value * multipliers[unit];
  const h = Math.floor(totalSeconds / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  const s = totalSeconds % 60;
  return `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
}

function getConfigField(
  config: Record<string, unknown> | undefined,
  field: string
): string {
  if (!config) return "";
  const value = config[field];
  if (value === undefined || value === null) return "";
  return String(value);
}

export interface CheckFormData {
  type?: CheckType;
  name?: string;
  slug?: string;
  checkGroupUid?: string;
  period?: string;
  config?: Record<string, unknown>;
  regions?: string[];
  reopenCooldownMultiplier?: number | null;
  maxAdaptiveIncrease?: number | null;
}

interface CheckFormProps {
  org: string;
  mode: "create" | "edit";
  initialData?: Check;
  checkGroups?: CheckGroup[];
  availableRegions?: RegionDefinition[];
  defaultRegions?: string[];
  onSubmit: (data: CheckFormData) => Promise<void>;
  isPending: boolean;
  onCancel: () => void;
}

export function CheckForm({
  org,
  mode,
  initialData,
  checkGroups,
  availableRegions,
  defaultRegions,
  onSubmit,
  isPending,
  onCancel,
}: CheckFormProps) {
  const initialType = (initialData?.type as CheckType) || "http";
  const showRegions = (availableRegions?.length ?? 0) > 1;

  const [type, setType] = useState<CheckType>(initialType);
  const [name, setName] = useState(initialData?.name || "");
  const [slug, setSlug] = useState(initialData?.slug || "");
  const [checkGroupUid, setCheckGroupUid] = useState(initialData?.checkGroupUid || "");
  const [period, setPeriod] = useState(initialData?.period || defaultPeriod(initialType));
  const initialPeriod = parsePeriod(initialData?.period || "00:05:00");
  const [periodValue, setPeriodValue] = useState(initialPeriod.value);
  const [periodUnit, setPeriodUnit] = useState<PeriodUnit>(initialPeriod.unit);
  const [url, setUrl] = useState(
    getConfigField(initialData?.config, "url")
  );
  const [host, setHost] = useState(
    getConfigField(initialData?.config, "host")
  );
  const [port, setPort] = useState(
    getConfigField(initialData?.config, "port")
  );
  const [domain, setDomain] = useState(
    getConfigField(initialData?.config, "domain")
  );
  const [method, setMethod] = useState(
    getConfigField(initialData?.config, "method") || "GET"
  );
  const [expectedStatus, setExpectedStatus] = useState(
    getConfigField(initialData?.config, "expectedStatus") || "200"
  );
  const [startTLS, setStartTLS] = useState(
    getConfigField(initialData?.config, "starttls") === "true"
  );
  const [tlsVerify, setTlsVerify] = useState(
    getConfigField(initialData?.config, "tls_verify") === "true"
  );
  const [ehloDomain, setEhloDomain] = useState(
    getConfigField(initialData?.config, "ehlo_domain")
  );
  const [expectGreeting, setExpectGreeting] = useState(
    getConfigField(initialData?.config, "expect_greeting")
  );
  const [checkAuth, setCheckAuth] = useState(
    getConfigField(initialData?.config, "check_auth") === "true"
  );
  const [username, setUsername] = useState(
    getConfigField(initialData?.config, "username")
  );
  const [password, setPassword] = useState(
    getConfigField(initialData?.config, "password")
  );
  const [database, setDatabase] = useState(
    getConfigField(initialData?.config, "database")
  );
  const [query, setQuery] = useState(
    getConfigField(initialData?.config, "query")
  );
  const [topic, setTopic] = useState(
    getConfigField(initialData?.config, "topic")
  );
  const [tls, setTls] = useState(
    getConfigField(initialData?.config, "tls") === "true"
  );
  const [script, setScript] = useState(
    getConfigField(initialData?.config, "script")
  );
  const [selectedRegions, setSelectedRegions] = useState<string[]>(
    initialData?.regions ?? defaultRegions ?? []
  );
  const [reopenCooldownMultiplier, setReopenCooldownMultiplier] = useState(
    initialData?.reopenCooldownMultiplier?.toString() ?? ""
  );
  const [maxAdaptiveIncrease, setMaxAdaptiveIncrease] = useState(
    initialData?.maxAdaptiveIncrease?.toString() ?? ""
  );
  const [error, setError] = useState<string | null>(null);

  const currentConfig = useMemo(() => {
    const cfg: Record<string, unknown> = {};
    switch (type) {
      case "http":
      case "ssl":
      case "websocket":
        if (url) cfg.url = url;
        if (type === "http") {
          if (method && method !== "GET") cfg.method = method;
          const statusCode = parseInt(expectedStatus, 10);
          if (!isNaN(statusCode) && statusCode !== 200) cfg.expectedStatus = statusCode;
          if (username) cfg.username = username;
          if (password) cfg.password = password;
        }
        break;
      case "tcp":
      case "udp":
      case "ftp":
        if (host) cfg.host = host;
        if (port) cfg.port = parseInt(port, 10);
        if (type === "ftp") {
          cfg.username = username || "anonymous";
          if (password) cfg.password = password;
        }
        break;
      case "ssh":
      case "pop3":
      case "imap":
      case "sftp":
        if (host) cfg.host = host;
        if (port) cfg.port = parseInt(port, 10);
        if (username) cfg.username = username;
        if (password) cfg.password = password;
        break;
      case "icmp":
        if (host) cfg.host = host;
        break;
      case "dns":
      case "domain":
        if (domain) cfg.domain = domain;
        break;
      case "smtp":
        if (host) cfg.host = host;
        if (port) cfg.port = parseInt(port, 10);
        if (startTLS) cfg.starttls = true;
        if (tlsVerify) cfg.tls_verify = true;
        if (ehloDomain) cfg.ehlo_domain = ehloDomain;
        if (expectGreeting) cfg.expect_greeting = expectGreeting;
        if (checkAuth) cfg.check_auth = true;
        if (username) cfg.username = username;
        if (password) cfg.password = password;
        break;
      case "postgresql":
        if (host) cfg.host = host;
        if (port) cfg.port = parseInt(port, 10);
        if (username) cfg.username = username;
        if (password) cfg.password = password;
        if (database) cfg.database = database;
        if (query) cfg.query = query;
        break;
      case "mqtt":
        if (host) cfg.host = host;
        if (port) cfg.port = parseInt(port, 10);
        if (username) cfg.username = username;
        if (password) cfg.password = password;
        if (topic) cfg.topic = topic;
        if (tls) cfg.tls = true;
        break;
      case "js":
        if (script) cfg.script = script;
        break;
    }
    return cfg;
  }, [type, url, host, port, domain, method, expectedStatus, username, password,
    startTLS, tlsVerify, ehloDomain, expectGreeting, checkAuth, database, query, topic, tls, script]);

  const fieldErrors = useCheckValidation(org, type, currentConfig, 300);

  const toggleRegion = (slug: string) => {
    setSelectedRegions((prev) =>
      prev.includes(slug) ? prev.filter((r) => r !== slug) : [...prev, slug]
    );
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);

    const config: Record<string, unknown> = {};

    switch (type) {
      case "http":
      case "ssl":
      case "websocket":
        if (!url) {
          setError("URL is required");
          return;
        }
        config.url = url;
        if (type === "http") {
          if (method && method !== "GET") {
            config.method = method;
          }
          const statusCode = parseInt(expectedStatus, 10);
          if (!isNaN(statusCode) && statusCode !== 200) {
            config.expectedStatus = statusCode;
          }
          if (username) config.username = username;
          if (password) config.password = password;
        }
        break;
      case "tcp":
      case "udp":
      case "ftp":
        if (!host) {
          setError("Host is required");
          return;
        }
        config.host = host;
        if (port) config.port = parseInt(port, 10);
        if (type === "ftp") {
          config.username = username || "anonymous";
          if (password) config.password = password;
        }
        break;
      case "ssh":
      case "pop3":
      case "imap":
        if (!host) {
          setError("Host is required");
          return;
        }
        config.host = host;
        if (port) config.port = parseInt(port, 10);
        if (username) config.username = username;
        if (password) config.password = password;
        break;
      case "sftp":
        if (!host) {
          setError("Host is required");
          return;
        }
        if (!username) {
          setError("Username is required");
          return;
        }
        config.host = host;
        if (port) config.port = parseInt(port, 10);
        config.username = username;
        if (password) config.password = password;
        break;
      case "icmp":
        if (!host) {
          setError("Host is required");
          return;
        }
        config.host = host;
        break;
      case "dns":
      case "domain":
        if (!domain) {
          setError("Domain is required");
          return;
        }
        config.domain = domain;
        break;
      case "smtp":
        if (!host) {
          setError("Host is required");
          return;
        }
        config.host = host;
        if (port) config.port = parseInt(port, 10);
        if (startTLS) config.starttls = true;
        if (tlsVerify) config.tls_verify = true;
        if (ehloDomain) config.ehlo_domain = ehloDomain;
        if (expectGreeting) config.expect_greeting = expectGreeting;
        if (checkAuth) config.check_auth = true;
        if (username) config.username = username;
        if (password) config.password = password;
        break;
      case "postgresql":
        if (!host) {
          setError("Host is required");
          return;
        }
        if (!username) {
          setError("Username is required");
          return;
        }
        config.host = host;
        if (port) config.port = parseInt(port, 10);
        config.username = username;
        if (password) config.password = password;
        if (database) config.database = database;
        if (query) config.query = query;
        break;
      case "mqtt":
        if (!host) {
          setError("Host is required");
          return;
        }
        config.host = host;
        if (port) config.port = parseInt(port, 10);
        if (username) config.username = username;
        if (password) config.password = password;
        if (topic) config.topic = topic;
        if (tls) config.tls = true;
        break;
      case "js":
        if (!script) {
          setError("Script is required");
          return;
        }
        config.script = script;
        break;
      case "heartbeat":
        // No config fields needed - token is auto-generated by the backend
        break;
    }

    try {
      await onSubmit({
        type: mode === "create" ? type : undefined,
        name: name || undefined,
        slug: slug || undefined,
        checkGroupUid: checkGroupUid || (mode === "edit" ? "" : undefined),
        period: type === "heartbeat" ? formatPeriod(periodValue, periodUnit) : period,
        // Don't send config for heartbeat edits — the token is managed by the backend
        ...(type === "heartbeat" && mode === "edit" ? {} : { config }),
        ...(showRegions ? { regions: selectedRegions } : {}),
        reopenCooldownMultiplier: reopenCooldownMultiplier !== "" ? parseInt(reopenCooldownMultiplier, 10) : null,
        maxAdaptiveIncrease: maxAdaptiveIncrease !== "" ? parseInt(maxAdaptiveIncrease, 10) : null,
      });
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message);
      } else {
        setError(
          mode === "create" ? "Failed to create check" : "Failed to update check"
        );
      }
    }
  };

  const renderConfigFields = () => {
    switch (type) {
      case "http":
        return (
          <>
            <div className="space-y-2">
              <Label>Request</Label>
              <div className="flex gap-2">
                <Select value={method} onValueChange={setMethod}>
                  <SelectTrigger className="w-28" data-testid="check-method-select">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"].map((m) => (
                      <SelectItem key={m} value={m}>{m}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Input
                  id="url"
                  type="url"
                  placeholder="https://example.com"
                  value={url}
                  onChange={(e) => setUrl(e.target.value)}
                  className={cn("flex-1", getFieldError(fieldErrors, "url") && "border-destructive")}
                  data-testid="check-url-input"
                />
              </div>
              {getFieldError(fieldErrors, "url") && (
                <p className="text-xs text-destructive">{getFieldError(fieldErrors, "url")}</p>
              )}
            </div>
            <div className="flex gap-4">
              <div className="space-y-2 flex-1">
                <Label htmlFor="expectedStatus">Expected Status</Label>
                <Input
                  id="expectedStatus"
                  type="number"
                  placeholder="200"
                  value={expectedStatus}
                  onChange={(e) => setExpectedStatus(e.target.value)}
                  data-testid="check-expected-status-input"
                />
              </div>
              <div className="space-y-2 flex-1">
                <Label htmlFor="period">Check Interval</Label>
                <Select value={period} onValueChange={setPeriod}>
                  <SelectTrigger data-testid="check-period-select">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {intervalOptions.map((opt) => (
                      <SelectItem key={opt.value} value={opt.value}>
                        {opt.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            </div>
            <div className="flex gap-4">
              <div className="space-y-2 flex-1">
                <Label htmlFor="username">Username (optional, Basic Auth)</Label>
                <Input
                  id="username"
                  type="text"
                  placeholder="user"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  data-testid="check-username-input"
                />
              </div>
              <div className="space-y-2 flex-1">
                <Label htmlFor="password">Password (optional)</Label>
                <Input
                  id="password"
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  data-testid="check-password-input"
                />
              </div>
            </div>
          </>
        );
      case "ssl":
        return (
          <div className="space-y-2">
            <Label htmlFor="url">URL</Label>
            <Input
              id="url"
              type="url"
              placeholder="https://example.com"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              className={cn(getFieldError(fieldErrors, "url") && "border-destructive")}
              data-testid="check-url-input"
            />
            {getFieldError(fieldErrors, "url") && (
              <p className="text-xs text-destructive">{getFieldError(fieldErrors, "url")}</p>
            )}
          </div>
        );
      case "websocket":
        return (
          <div className="space-y-2">
            <Label htmlFor="url">URL</Label>
            <Input
              id="url"
              type="url"
              placeholder="wss://example.com/ws"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              className={cn(getFieldError(fieldErrors, "url") && "border-destructive")}
              data-testid="check-url-input"
            />
            {getFieldError(fieldErrors, "url") && (
              <p className="text-xs text-destructive">{getFieldError(fieldErrors, "url")}</p>
            )}
          </div>
        );
      case "tcp":
      case "udp":
        return (
          <div className="space-y-2">
            <Label>Host</Label>
            <div className="flex gap-2">
              <Input
                id="host"
                type="text"
                placeholder={type === "udp" ? "8.8.8.8" : "example.com"}
                value={host}
                onChange={(e) => setHost(e.target.value)}
                className={cn("flex-1", getFieldError(fieldErrors, "host") && "border-destructive")}
                data-testid="check-host-input"
              />
              <Input
                id="port"
                type="number"
                placeholder={type === "udp" ? "53" : "443"}
                value={port}
                onChange={(e) => setPort(e.target.value)}
                className={cn("w-24", getFieldError(fieldErrors, "port") && "border-destructive")}
                data-testid="check-port-input"
              />
            </div>
            {getFieldError(fieldErrors, "host") && (
              <p className="text-xs text-destructive">{getFieldError(fieldErrors, "host")}</p>
            )}
            {getFieldError(fieldErrors, "port") && (
              <p className="text-xs text-destructive">{getFieldError(fieldErrors, "port")}</p>
            )}
          </div>
        );
      case "ssh":
      case "pop3":
      case "imap":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input
                  id="host"
                  type="text"
                  placeholder={
                    type === "ssh"
                      ? "server.example.com"
                      : "mail.example.com"
                  }
                  value={host}
                  onChange={(e) => setHost(e.target.value)}
                  className={cn("flex-1", getFieldError(fieldErrors, "host") && "border-destructive")}
                  data-testid="check-host-input"
                />
                <Input
                  id="port"
                  type="number"
                  placeholder={
                    type === "ssh"
                      ? "22"
                      : type === "pop3"
                        ? "110"
                        : "143"
                  }
                  value={port}
                  onChange={(e) => setPort(e.target.value)}
                  className={cn("w-24", getFieldError(fieldErrors, "port") && "border-destructive")}
                  data-testid="check-port-input"
                />
              </div>
              {getFieldError(fieldErrors, "host") && (
                <p className="text-xs text-destructive">{getFieldError(fieldErrors, "host")}</p>
              )}
              {getFieldError(fieldErrors, "port") && (
                <p className="text-xs text-destructive">{getFieldError(fieldErrors, "port")}</p>
              )}
            </div>
            <div className="space-y-2">
              <Label htmlFor="username">Username (optional)</Label>
              <Input
                id="username"
                type="text"
                placeholder="user"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                data-testid="check-username-input"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="password">Password (optional)</Label>
              <Input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                data-testid="check-password-input"
              />
            </div>
          </>
        );
      case "ftp":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input
                  id="host"
                  type="text"
                  placeholder="ftp.example.com"
                  value={host}
                  onChange={(e) => setHost(e.target.value)}
                  className="flex-1"
                  data-testid="check-host-input"
                />
                <Input
                  id="port"
                  type="number"
                  placeholder="21"
                  value={port}
                  onChange={(e) => setPort(e.target.value)}
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
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                data-testid="check-username-input"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="password">Password (optional)</Label>
              <Input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                data-testid="check-password-input"
              />
            </div>
          </>
        );
      case "sftp":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input
                  id="host"
                  type="text"
                  placeholder="sftp.example.com"
                  value={host}
                  onChange={(e) => setHost(e.target.value)}
                  className="flex-1"
                  data-testid="check-host-input"
                />
                <Input
                  id="port"
                  type="number"
                  placeholder="22"
                  value={port}
                  onChange={(e) => setPort(e.target.value)}
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
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                data-testid="check-username-input"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="password">Password</Label>
              <Input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                data-testid="check-password-input"
              />
            </div>
          </>
        );
      case "icmp":
        return (
          <div className="space-y-2">
            <Label htmlFor="host">Host</Label>
            <Input
              id="host"
              type="text"
              placeholder="example.com"
              value={host}
              onChange={(e) => setHost(e.target.value)}
              className={cn(getFieldError(fieldErrors, "host") && "border-destructive")}
              data-testid="check-host-input"
            />
            {getFieldError(fieldErrors, "host") && (
              <p className="text-xs text-destructive">{getFieldError(fieldErrors, "host")}</p>
            )}
          </div>
        );
      case "dns":
      case "domain":
        return (
          <div className="space-y-2">
            <Label htmlFor="domain">Domain</Label>
            <Input
              id="domain"
              type="text"
              placeholder="example.com"
              value={domain}
              onChange={(e) => setDomain(e.target.value)}
              className={cn(getFieldError(fieldErrors, "domain") && "border-destructive")}
              data-testid="check-domain-input"
            />
            {getFieldError(fieldErrors, "domain") && (
              <p className="text-xs text-destructive">{getFieldError(fieldErrors, "domain")}</p>
            )}
          </div>
        );
      case "smtp":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input
                  id="host"
                  type="text"
                  placeholder="mail.example.com"
                  value={host}
                  onChange={(e) => setHost(e.target.value)}
                  className="flex-1"
                  data-testid="check-host-input"
                />
                <Input
                  id="port"
                  type="number"
                  placeholder="25"
                  value={port}
                  onChange={(e) => setPort(e.target.value)}
                  className="w-24"
                  data-testid="check-port-input"
                />
              </div>
            </div>
            <div className="space-y-3">
              <label className="flex items-center gap-2">
                <Checkbox
                  checked={startTLS}
                  onCheckedChange={(v) => setStartTLS(v === true)}
                  data-testid="check-starttls-checkbox"
                />
                <span className="text-sm">Use STARTTLS</span>
              </label>
              <label className="flex items-center gap-2">
                <Checkbox
                  checked={tlsVerify}
                  onCheckedChange={(v) => setTlsVerify(v === true)}
                  data-testid="check-tls-verify-checkbox"
                />
                <span className="text-sm">Verify TLS certificate</span>
              </label>
              <label className="flex items-center gap-2">
                <Checkbox
                  checked={checkAuth}
                  onCheckedChange={(v) => setCheckAuth(v === true)}
                  data-testid="check-auth-checkbox"
                />
                <span className="text-sm">Check AUTH support</span>
              </label>
            </div>
            <div className="space-y-2">
              <Label htmlFor="ehloDomain">EHLO Domain (optional)</Label>
              <Input
                id="ehloDomain"
                type="text"
                placeholder="example.com"
                value={ehloDomain}
                onChange={(e) => setEhloDomain(e.target.value)}
                data-testid="check-ehlo-domain-input"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="expectGreeting">Expected Greeting (optional)</Label>
              <Input
                id="expectGreeting"
                type="text"
                placeholder="220"
                value={expectGreeting}
                onChange={(e) => setExpectGreeting(e.target.value)}
                data-testid="check-expect-greeting-input"
              />
            </div>
            <div className="flex gap-4">
              <div className="space-y-2 flex-1">
                <Label htmlFor="username">Username (optional, AUTH PLAIN)</Label>
                <Input
                  id="username"
                  type="text"
                  placeholder="user@example.com"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  data-testid="check-username-input"
                />
              </div>
              <div className="space-y-2 flex-1">
                <Label htmlFor="password">Password (optional)</Label>
                <Input
                  id="password"
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  data-testid="check-password-input"
                />
              </div>
            </div>
          </>
        );
      case "postgresql":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input
                  id="host"
                  type="text"
                  placeholder="db.example.com"
                  value={host}
                  onChange={(e) => setHost(e.target.value)}
                  className="flex-1"
                  data-testid="check-host-input"
                />
                <Input
                  id="port"
                  type="number"
                  placeholder="5432"
                  value={port}
                  onChange={(e) => setPort(e.target.value)}
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
                placeholder="postgres"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                data-testid="check-username-input"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="password">Password (optional)</Label>
              <Input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                data-testid="check-password-input"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="database">Database (optional)</Label>
              <Input
                id="database"
                type="text"
                placeholder="postgres"
                value={database}
                onChange={(e) => setDatabase(e.target.value)}
                data-testid="check-database-input"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="query">Query (optional)</Label>
              <Input
                id="query"
                type="text"
                placeholder="SELECT 1"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                data-testid="check-query-input"
              />
            </div>
          </>
        );
      case "mqtt":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input
                  id="host"
                  type="text"
                  placeholder="broker.example.com"
                  value={host}
                  onChange={(e) => setHost(e.target.value)}
                  className={cn("flex-1", getFieldError(fieldErrors, "host") && "border-destructive")}
                  data-testid="check-host-input"
                />
                <Input
                  id="port"
                  type="number"
                  placeholder="1883"
                  value={port}
                  onChange={(e) => setPort(e.target.value)}
                  className={cn("w-24", getFieldError(fieldErrors, "port") && "border-destructive")}
                  data-testid="check-port-input"
                />
              </div>
              {getFieldError(fieldErrors, "host") && (
                <p className="text-xs text-destructive">{getFieldError(fieldErrors, "host")}</p>
              )}
              {getFieldError(fieldErrors, "port") && (
                <p className="text-xs text-destructive">{getFieldError(fieldErrors, "port")}</p>
              )}
            </div>
            <div className="flex gap-4">
              <div className="space-y-2 flex-1">
                <Label htmlFor="username">Username (optional)</Label>
                <Input
                  id="username"
                  type="text"
                  placeholder="user"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  data-testid="check-username-input"
                />
              </div>
              <div className="space-y-2 flex-1">
                <Label htmlFor="password">Password (optional)</Label>
                <Input
                  id="password"
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
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
                value={topic}
                onChange={(e) => setTopic(e.target.value)}
                className={cn(getFieldError(fieldErrors, "topic") && "border-destructive")}
                data-testid="check-topic-input"
              />
              {getFieldError(fieldErrors, "topic") && (
                <p className="text-xs text-destructive">{getFieldError(fieldErrors, "topic")}</p>
              )}
            </div>
            <div className="space-y-3">
              <label className="flex items-center gap-2">
                <Checkbox
                  checked={tls}
                  onCheckedChange={(v) => setTls(v === true)}
                  data-testid="check-tls-checkbox"
                />
                <span className="text-sm">Use TLS (port defaults to 8883)</span>
              </label>
            </div>
          </>
        );
      case "js":
        return (
          <div className="space-y-2">
            <Label htmlFor="script">Script</Label>
            <CodeMirror
              value={script}
              onChange={(value) => setScript(value)}
              extensions={[javascript()]}
              theme={document.documentElement.classList.contains("dark") ? "dark" : "light"}
              height="200px"
              className={cn(
                "rounded-md border text-sm",
                getFieldError(fieldErrors, "script") &&
                  "border-destructive",
              )}
              data-testid="check-script"
            />
            {getFieldError(fieldErrors, "script") && (
              <p className="text-xs text-destructive">
                {getFieldError(fieldErrors, "script")}
              </p>
            )}
            <p className="text-xs text-muted-foreground">
              JavaScript script that returns an object with status
              (&quot;up&quot;, &quot;down&quot;, or &quot;error&quot;),
              optional metrics, and optional output.
            </p>
          </div>
        );
      case "heartbeat":
        return (
          <p className="text-sm text-muted-foreground">
            No additional configuration needed. A heartbeat URL will be generated after creation.
          </p>
        );
    }
  };

  const isEdit = mode === "edit";
  const title = isEdit ? "Edit Check" : "New Check";
  const subtitle = isEdit
    ? "Update the monitoring check parameters"
    : "Create a new monitoring check";
  const submitLabel = isEdit ? "Save Changes" : "Create Check";
  const pendingLabel = isEdit ? "Saving..." : "Creating...";

  return (
    <div className="space-y-6 max-w-2xl">
      <div className="flex items-center gap-4">
        <Button variant="ghost" size="icon" onClick={onCancel}>
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <div>
          <h1 className="text-3xl font-bold tracking-tight">{title}</h1>
          <p className="text-muted-foreground">{subtitle}</p>
        </div>
      </div>

      <Card>
        <form onSubmit={handleSubmit}>
          <CardHeader>
            <CardTitle>Check Configuration</CardTitle>
            <CardDescription>
              Configure the monitoring check parameters
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            {error && (
              <Alert variant="destructive">
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}

            <div className="space-y-2">
              <Label htmlFor="type">Check Type</Label>
              {isEdit ? (
                <Input
                  id="type"
                  value={
                    checkTypes.find((t) => t.value === type)?.label || type
                  }
                  disabled
                  data-testid="check-type-select"
                />
              ) : (
                <Select
                  value={type}
                  onValueChange={(v) => {
                    const t = v as CheckType;
                    setType(t);
                    setPeriod(defaultPeriod(t));
                  }}
                >
                  <SelectTrigger data-testid="check-type-select">
                    <SelectValue>
                      {checkTypes.find((t) => t.value === type)?.label || type}
                    </SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    {checkTypes.map((t) => (
                      <SelectItem key={t.value} value={t.value}>
                        <div className="font-medium">{t.label}</div>
                        <div className="text-xs text-muted-foreground">
                          {t.description}
                        </div>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </div>

            {renderConfigFields()}

            {type !== "http" && (
              <div className="space-y-2">
                <Label htmlFor="period">
                  {type === "heartbeat" ? "Expected Interval" : "Check Interval"}
                </Label>
                {type === "heartbeat" ? (
                  <div className="flex gap-2">
                    <Input
                      id="period"
                      type="number"
                      min={1}
                      value={periodValue}
                      onChange={(e) => setPeriodValue(parseInt(e.target.value, 10) || 1)}
                      className="w-24"
                      data-testid="check-period-input"
                    />
                    <Select value={periodUnit} onValueChange={(v) => setPeriodUnit(v as PeriodUnit)}>
                      <SelectTrigger data-testid="check-period-unit-select" className="flex-1">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {periodUnits.map((u) => (
                          <SelectItem key={u.value} value={u.value}>
                            {u.label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                ) : (
                  <Select value={period} onValueChange={setPeriod}>
                    <SelectTrigger data-testid="check-period-select">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {intervalOptions.map((opt) => (
                        <SelectItem key={opt.value} value={opt.value}>
                          {opt.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
                {type === "heartbeat" && (
                  <p className="text-xs text-muted-foreground">
                    Check will be marked as down if no heartbeat is received within this interval
                  </p>
                )}
              </div>
            )}

            <div className="space-y-2">
              <Label htmlFor="name">Name {mode === "create" && "(optional)"}</Label>
              <Input
                id="name"
                type="text"
                placeholder="My Check"
                value={name}
                onChange={(e) => setName(e.target.value)}
                data-testid="check-name-input"
              />
              {mode === "create" && (
                <p className="text-xs text-muted-foreground">
                  If not provided, a name will be auto-generated
                </p>
              )}
            </div>

            <div className="space-y-2">
              <Label htmlFor="slug">Slug {mode === "create" && "(optional)"}</Label>
              <Input
                id="slug"
                type="text"
                placeholder="my-check"
                value={slug}
                onChange={(e) => setSlug(e.target.value)}
                data-testid="check-slug-input"
              />
              <p className="text-xs text-muted-foreground">
                URL-friendly identifier for the check
              </p>
            </div>

            {checkGroups && checkGroups.length > 0 && (
              <div className="space-y-2">
                <Label htmlFor="group">Group (optional)</Label>
                <Select
                  value={checkGroupUid || "none"}
                  onValueChange={(v) => setCheckGroupUid(v === "none" ? "" : v)}
                >
                  <SelectTrigger data-testid="check-group-select">
                    <SelectValue placeholder="No group" />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">No group</SelectItem>
                    {checkGroups.map((g) => (
                      <SelectItem key={g.uid} value={g.uid}>
                        {g.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            )}

            <div className="space-y-2">
              <Label className="text-base font-medium">Adaptive Resolution</Label>
              <div className="grid grid-cols-2 gap-4">
                <div className="space-y-1">
                  <Label htmlFor="reopenCooldownMultiplier" className="text-sm">
                    Reopen cooldown multiplier
                  </Label>
                  <Input
                    id="reopenCooldownMultiplier"
                    type="number"
                    min={0}
                    placeholder="5 (default)"
                    value={reopenCooldownMultiplier}
                    onChange={(e) => setReopenCooldownMultiplier(e.target.value)}
                  />
                  <p className="text-xs text-muted-foreground">
                    How many check periods to wait before closing a resolved incident. 0 = never reopen.
                  </p>
                </div>
                <div className="space-y-1">
                  <Label htmlFor="maxAdaptiveIncrease" className="text-sm">
                    Max adaptive increase
                  </Label>
                  <Input
                    id="maxAdaptiveIncrease"
                    type="number"
                    min={0}
                    placeholder="5 (default)"
                    value={maxAdaptiveIncrease}
                    onChange={(e) => setMaxAdaptiveIncrease(e.target.value)}
                  />
                  <p className="text-xs text-muted-foreground">
                    Extra consecutive successes required per relapse. 0 = fixed threshold.
                  </p>
                </div>
              </div>
            </div>

            {showRegions && (
              <div className="space-y-2">
                <Label>Regions</Label>
                <div className="grid grid-cols-2 gap-2">
                  {availableRegions?.map((region) => (
                    <label
                      key={region.slug}
                      className="flex items-center gap-2 rounded-md border p-2 cursor-pointer hover:bg-muted/50"
                    >
                      <Checkbox
                        checked={selectedRegions.includes(region.slug)}
                        onCheckedChange={() => toggleRegion(region.slug)}
                      />
                      <span className="text-sm">
                        {region.emoji} {region.name}
                      </span>
                    </label>
                  ))}
                </div>
                <p className="text-xs text-muted-foreground">
                  Select the regions where this check should run
                </p>
              </div>
            )}
          </CardContent>
          <CardFooter className="flex justify-end gap-2">
            <Button type="button" variant="outline" onClick={onCancel}>
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={isPending}
              data-testid="check-submit-button"
            >
              {isPending ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  {pendingLabel}
                </>
              ) : (
                submitLabel
              )}
            </Button>
          </CardFooter>
        </form>
      </Card>
    </div>
  );
}
