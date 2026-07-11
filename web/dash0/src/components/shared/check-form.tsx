import { useState, useMemo, useRef, useEffect } from "react";
import { useTranslation } from "react-i18next";
import { ArrowLeft, Loader2, ChevronsUpDown, Check, Search, Plus, Trash2 } from "lucide-react";
import CodeMirror from "@uiw/react-codemirror";
import { javascript } from "@codemirror/lang-javascript";
import { useCheckValidation, getFieldError } from "@/hooks/use-check-validation";
import { cn } from "@/lib/utils";
import { describePeriod, formatDuration } from "@/lib/period-estimate";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
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
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Checkbox } from "@/components/ui/checkbox";
import { Switch } from "@/components/ui/switch";
import { CollapsibleSection } from "@/components/ui/collapsible-section";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { LabelInput } from "@/components/shared/label-input";
import { ApiError } from "@/api/client";
import type { Check as CheckModel, CheckGroup, RegionDefinition, SampleConfig } from "@/api/hooks";
import {
  useCheckTypes,
  useSampleConfigs,
  useIntegrations,
  useCheckConnections,
  useCheckDependencies,
  canNotify,
  canSource,
} from "@/api/hooks";
import { useEmailAddressDomain } from "@/api/email-inbox";
import {
  IntegrationIcon,
  integrationLabel,
} from "@/components/integrations/integration-icon";
import { Link } from "@tanstack/react-router";
import { Badge } from "@/components/ui/badge";
import { CheckPicker } from "@/components/shared/check-picker";
import { requiredFieldError } from "@/components/checks/form/serialize";
import { FreeboxLanDiscovery } from "@/components/shared/freebox-lan-discovery";
import type { FreeboxLanHost } from "@/api/hooks";
import { X } from "lucide-react";

type CheckType = "http" | "tcp" | "icmp" | "dns" | "ssl" | "heartbeat" | "email" | "domain" | "smtp" | "udp" | "ssh" | "pop3" | "imap" | "websocket" | "postgresql" | "mysql" | "redis" | "mongodb" | "ftp" | "sftp" | "js" | "mssql" | "oracle" | "grpc" | "kafka" | "mqtt" | "a2s" | "minecraft" | "rabbitmq" | "snmp" | "docker" | "browser" | "freebox_line" | "dnsbl" | "sip" | "ntp" | "rdp" | "sleep";

// Fallback defaults when API data isn't available
const defaultPeriodSeconds: Record<string, number> = {
  domain: 86400,
  ssl: 21600,
  dns: 300,
};
const globalDefaultPeriodSeconds = 60;
// Server-enforced global floor (spec 2026-07-01-04): heavy types carry their
// own minPeriodSeconds via the check-types API (browser 60s, js 30s).
const globalMinPeriodSeconds = 10;

const checkTypes: { value: CheckType; label: string; description: string; synthetic?: boolean }[] = [
  { value: "http", label: "HTTP", description: "Monitor HTTP/HTTPS endpoints" },
  { value: "tcp", label: "TCP", description: "Check TCP port connectivity" },
  { value: "icmp", label: "ICMP", description: "Ping hosts using ICMP" },
  { value: "dns", label: "DNS", description: "Verify DNS resolution" },
  { value: "ssl", label: "SSL", description: "Check SSL certificate validity" },
  { value: "heartbeat", label: "Heartbeat", description: "Monitor via incoming pings" },
  { value: "email", label: "Email", description: "Receive status updates via incoming email" },
  { value: "domain", label: "Domain", description: "Monitor domain name expiration" },
  { value: "smtp", label: "SMTP", description: "Check SMTP server availability" },
  { value: "udp", label: "UDP", description: "Check UDP port reachability" },
  { value: "ssh", label: "SSH", description: "Check SSH server availability" },
  { value: "pop3", label: "POP3", description: "Check POP3 server availability" },
  { value: "imap", label: "IMAP", description: "Check IMAP server availability" },
  { value: "websocket", label: "WebSocket", description: "Check WebSocket connectivity" },
  { value: "postgresql", label: "PostgreSQL", description: "Check PostgreSQL database health" },
  { value: "mysql", label: "MySQL", description: "Check MySQL/MariaDB database health" },
  { value: "redis", label: "Redis", description: "Check Redis server health" },
  { value: "mongodb", label: "MongoDB", description: "Check MongoDB database health" },
  { value: "ftp", label: "FTP", description: "Check FTP server availability" },
  { value: "sftp", label: "SFTP", description: "Check SFTP server availability" },
  { value: "js", label: "JavaScript", description: "Run custom JavaScript monitoring scripts" },
  { value: "mssql", label: "MSSQL", description: "Check Microsoft SQL Server health" },
  { value: "oracle", label: "Oracle", description: "Check Oracle Database health" },
  { value: "grpc", label: "gRPC", description: "Check gRPC service health" },
  { value: "kafka", label: "Kafka", description: "Check Kafka cluster health" },
  { value: "mqtt", label: "MQTT", description: "Check MQTT broker connectivity" },
  { value: "a2s", label: "A2S Game Server", description: "Monitor Source engine game servers via A2S" },
  { value: "minecraft", label: "Minecraft", description: "Monitor Minecraft servers (Java + Bedrock)" },
  { value: "rabbitmq", label: "RabbitMQ", description: "Check RabbitMQ server health" },
  { value: "snmp", label: "SNMP", description: "Monitor devices via SNMP" },
  { value: "docker", label: "Docker", description: "Monitor Docker container health" },
  { value: "browser", label: "Browser", description: "Monitor pages with headless Chrome" },
  { value: "freebox_line", label: "Freebox Line", description: "Monitor xDSL/FTTH line quality via Freebox OS" },
  { value: "dnsbl", label: "DNSBL", description: "Check if an IP/domain is on DNS blocklists" },
  { value: "sip", label: "SIP", description: "Check SIP server reachability and registration" },
  { value: "ntp", label: "NTP", description: "Monitor NTP time servers" },
  { value: "rdp", label: "RDP", description: "Monitor RDP (Remote Desktop) servers" },
  { value: "sleep", label: "Sleep", description: "Sleep for a fixed duration (synthetic/testing, no network I/O)", synthetic: true },
];

// isPassiveType reports whether a check type uses the "expected interval"
// UX (heartbeat / email) rather than the active polling interval.
function isPassiveType(t: CheckType): boolean {
  return t === "heartbeat" || t === "email";
}

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

function secondsToHMS(seconds: number): string {
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = seconds % 60;
  return `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
}

function hmsToSeconds(hms: string): number {
  const [h, m, s] = hms.split(":").map(Number);
  return h * 3600 + m * 60 + s;
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

// durationStringToSeconds converts a simple Go duration string ("120s", "2m")
// into whole seconds for a numeric input. Returns "" when empty/unparseable so
// the input stays blank rather than showing 0.
function durationStringToSeconds(raw: string): string {
  if (!raw) return "";
  const match = raw.match(/^(\d+(?:\.\d+)?)(s|m|h)$/);
  if (!match) return "";
  const value = parseFloat(match[1]);
  const unit = match[2];
  const seconds = unit === "h" ? value * 3600 : unit === "m" ? value * 60 : value;
  return String(Math.round(seconds));
}

// splitBlocklists turns the DNSBL blocklists textarea (comma/newline separated)
// into a trimmed, de-duplicated array, dropping empty entries.
function splitBlocklists(raw: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const z of raw.split(/[\n,]/)) {
    const zone = z.trim();
    if (zone && !seen.has(zone)) {
      seen.add(zone);
      out.push(zone);
    }
  }
  return out;
}

function buildIntervalOptions(minSeconds: number, maxSeconds: number): { value: string; label: string }[] {
  const allOptions = [
    { seconds: 5, value: "00:00:05", label: "5 seconds" },
    { seconds: 10, value: "00:00:10", label: "10 seconds" },
    { seconds: 30, value: "00:00:30", label: "30 seconds" },
    { seconds: 60, value: "00:01:00", label: "1 minute" },
    { seconds: 300, value: "00:05:00", label: "5 minutes" },
    { seconds: 600, value: "00:10:00", label: "10 minutes" },
    { seconds: 1800, value: "00:30:00", label: "30 minutes" },
    { seconds: 3600, value: "01:00:00", label: "1 hour" },
    { seconds: 21600, value: "06:00:00", label: "6 hours" },
    { seconds: 43200, value: "12:00:00", label: "12 hours" },
    { seconds: 86400, value: "24:00:00", label: "24 hours" },
  ];

  return allOptions
    .filter((opt) => opt.seconds >= minSeconds && (maxSeconds === 0 || opt.seconds <= maxSeconds))
    .map(({ value, label }) => ({ value, label }));
}

export interface CheckFormData {
  type?: CheckType;
  enabled?: boolean;
  name?: string;
  slug?: string;
  checkGroupUid?: string;
  period?: string;
  config?: Record<string, unknown>;
  regions?: string[];
  reopenCooldownMultiplier?: number | null;
  flappingWindowSeconds?: number | null;
  flapBackoffFactor?: number | null;
  maxRecoveryMultiplier?: number | null;
  confirmationPeriodSeconds?: number;
  recoveryPeriodSeconds?: number;
  labels?: Record<string, string>;
  connectionUids?: string[];
  dependsOnParentUids?: string[];
  initialDependsOnParentUids?: string[];
}

interface CheckFormProps {
  org: string;
  mode: "create" | "edit";
  initialData?: CheckModel;
  checkGroups?: CheckGroup[];
  availableRegions?: RegionDefinition[];
  defaultRegions?: string[];
  onSubmit: (data: CheckFormData) => Promise<void>;
  isPending: boolean;
  onCancel: () => void;
  onTypeChange?: (type: CheckType) => void;
  /** `?section=<name>` deep-link: expand + scroll that collapsible on mount. */
  initialSection?: string;
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
  onTypeChange,
  initialSection,
}: CheckFormProps) {
  const { t } = useTranslation("checks");
  // Fetch enabled check types from API; fall back to hardcoded list if unavailable
  const { data: apiCheckTypes } = useCheckTypes(org);
  const { data: emailDomain } = useEmailAddressDomain();
  const availableCheckTypes = useMemo(() => {
    if (!apiCheckTypes || apiCheckTypes.length === 0) return checkTypes;
    const enabledSet = new Set(
      apiCheckTypes.filter((t) => t.enabled).map((t) => t.type)
    );
    // Build list from API data, matching against local entries for labels
    return checkTypes.filter((ct) => enabledSet.has(ct.value));
  }, [apiCheckTypes]);

  // Build a lookup map for API check type info (for period constraints & samples)
  const checkTypeInfoMap = useMemo(() => {
    const map = new Map<string, (typeof apiCheckTypes extends (infer T)[] | undefined ? T : never)>();
    if (apiCheckTypes) {
      for (const ct of apiCheckTypes) {
        map.set(ct.type, ct);
      }
    }
    return map;
  }, [apiCheckTypes]);

  const initialType = (initialData?.type as CheckType) || "http";
  const showRegions = (availableRegions?.length ?? 0) > 1;

  // Get period constraints for a given type
  function getPeriodConstraints(t: string) {
    const info = checkTypeInfoMap.get(t);
    const minSec = info?.minPeriodSeconds || globalMinPeriodSeconds;
    const maxSec = info?.maxPeriodSeconds || 0;
    const defSec = info?.defaultPeriodSeconds || defaultPeriodSeconds[t] || globalDefaultPeriodSeconds;
    return { minSec, maxSec, defSec };
  }

  function getDefaultPeriodHMS(t: string): string {
    const { defSec } = getPeriodConstraints(t);
    return secondsToHMS(defSec);
  }

  const slugRegex = /^[a-z][a-z0-9-]{2,49}$/;
  const uuidRegex = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

  function validateSlug(value: string): string | null {
    if (!value) return null; // empty is OK (auto-generated)
    if (uuidRegex.test(value)) return "Slug must not be a UUID";
    if (!slugRegex.test(value)) {
      if (value.length < 3) return "Slug must be at least 3 characters";
      if (value.length > 50) return "Slug must be at most 50 characters";
      if (!/^[a-z]/.test(value)) return "Slug must start with a lowercase letter";
      return "Slug must contain only lowercase letters, digits, and hyphens";
    }
    return null;
  }

  const [type, setType] = useState<CheckType>(initialType);
  const [enabled, setEnabled] = useState(initialData?.enabled ?? true);
  const [name, setName] = useState(initialData?.name || "");
  const [slug, setSlug] = useState(initialData?.slug || "");
  const slugError = validateSlug(slug);
  const [checkGroupUid, setCheckGroupUid] = useState(initialData?.checkGroupUid || "");
  const [labels, setLabels] = useState<Record<string, string>>(initialData?.labels ?? {});
  const [labelsDirty, setLabelsDirty] = useState(false);

  const { data: connections } = useIntegrations(org);
  const { data: existingBindings } = useCheckConnections(
    org,
    mode === "edit" ? initialData?.uid : undefined,
  );
  const [connectionUids, setConnectionUids] = useState<string[] | null>(null);

  // Seed once per fetch: in create mode preselect defaults; in edit mode use the
  // current bindings.
  useEffect(() => {
    if (connectionUids !== null) return;
    if (mode === "create") {
      if (!connections) return;
      // Only notify-capable integrations can be a default notification target.
      setConnectionUids(
        connections
          .filter((c) => c.isDefault && c.enabled && canNotify(c.type))
          .map((c) => c.uid),
      );
      return;
    }
    if (existingBindings) {
      setConnectionUids(existingBindings.map((c) => c.uid));
    }
  }, [mode, connections, existingBindings, connectionUids]);

  function toggleConnection(uid: string) {
    setConnectionUids((prev) => {
      const cur = prev ?? [];
      return cur.includes(uid) ? cur.filter((u) => u !== uid) : [...cur, uid];
    });
  }

  const { data: existingDeps } = useCheckDependencies(
    org,
    mode === "edit" ? initialData?.uid : undefined,
  );
  const [dependsOnParents, setDependsOnParents] = useState<
    { uid: string; label: string }[] | null
  >(null);
  const initialParentUids = useMemo(
    () => (existingDeps?.dependsOn ?? []).map((e) => e.parentCheck.uid),
    [existingDeps],
  );

  useEffect(() => {
    if (dependsOnParents !== null) return;
    if (mode === "create") {
      setDependsOnParents([]);
      return;
    }
    if (existingDeps) {
      setDependsOnParents(
        existingDeps.dependsOn.map((e) => ({
          uid: e.parentCheck.uid,
          label: e.parentCheck.name || e.parentCheck.slug,
        })),
      );
    }
  }, [mode, existingDeps, dependsOnParents]);

  function addParent(uid: string, label: string) {
    setDependsOnParents((prev) => {
      const cur = prev ?? [];
      if (cur.some((p) => p.uid === uid)) return cur;
      return [...cur, { uid, label }];
    });
  }
  function removeParent(uid: string) {
    setDependsOnParents((prev) => (prev ?? []).filter((p) => p.uid !== uid));
  }
  const [period, setPeriod] = useState(initialData?.period || getDefaultPeriodHMS(initialType));
  const initialPeriod = parsePeriod(initialData?.period || "00:05:00");
  const [periodValue, setPeriodValue] = useState(initialPeriod.value);
  const [periodUnit, setPeriodUnit] = useState<PeriodUnit>(initialPeriod.unit);
  // Optional per-check timeout — stored as a Go duration string in config
  // ("10s"); the input edits whole seconds. Empty = unset (checker default).
  const [timeoutSeconds, setTimeoutSeconds] = useState(
    durationStringToSeconds(getConfigField(initialData?.config, "timeout")),
  );
  const [url, setUrl] = useState(getConfigField(initialData?.config, "url"));
  const [host, setHost] = useState(getConfigField(initialData?.config, "host"));
  const [port, setPort] = useState(getConfigField(initialData?.config, "port"));
  const [domain, setDomain] = useState(getConfigField(initialData?.config, "domain"));
  const [method, setMethod] = useState(getConfigField(initialData?.config, "method") || "GET");
  const [expectedStatus, setExpectedStatus] = useState(getConfigField(initialData?.config, "expectedStatus") || "200");
  const [startTLS, setStartTLS] = useState(getConfigField(initialData?.config, "starttls") === "true");
  const [tlsVerify, setTlsVerify] = useState(getConfigField(initialData?.config, "tls_verify") === "true");
  const [ehloDomain, setEhloDomain] = useState(getConfigField(initialData?.config, "ehlo_domain"));
  const [expectGreeting, setExpectGreeting] = useState(getConfigField(initialData?.config, "expect_greeting"));
  const [checkAuth, setCheckAuth] = useState(getConfigField(initialData?.config, "check_auth") === "true");
  const [username, setUsername] = useState(getConfigField(initialData?.config, "username"));
  const [password, setPassword] = useState(getConfigField(initialData?.config, "password"));
  const [database, setDatabase] = useState(getConfigField(initialData?.config, "database"));
  const [query, setQuery] = useState(getConfigField(initialData?.config, "query"));
  const [vhost, setVhost] = useState(getConfigField(initialData?.config, "vhost"));
  const [queue, setQueue] = useState(getConfigField(initialData?.config, "queue"));
  const [script, setScript] = useState(getConfigField(initialData?.config, "script"));
  const [serviceName, setServiceName] = useState(getConfigField(initialData?.config, "serviceName"));
  const [tls, setTls] = useState(getConfigField(initialData?.config, "tls") === "true");
  const [minPlayers, setMinPlayers] = useState(getConfigField(initialData?.config, "minPlayers"));
  const [maxPlayersField, setMaxPlayersField] = useState(getConfigField(initialData?.config, "maxPlayers"));
  const [edition, setEdition] = useState(getConfigField(initialData?.config, "edition") || "java");
  const [brokers, setBrokers] = useState(
    Array.isArray(initialData?.config?.brokers)
      ? (initialData.config.brokers as string[]).join(", ")
      : getConfigField(initialData?.config, "brokers")
  );
  const [topic, setTopic] = useState(getConfigField(initialData?.config, "topic"));
  const [produceTest, setProduceTest] = useState(getConfigField(initialData?.config, "produceTest") === "true");
  const [containerName, setContainerName] = useState(getConfigField(initialData?.config, "containerName"));
  const [containerId, setContainerId] = useState(getConfigField(initialData?.config, "containerId"));
  const [restartLoopMinRestarts, setRestartLoopMinRestarts] = useState(
    getConfigField(initialData?.config, "restartLoopMinRestarts"),
  );
  // Window is a Go duration string in config ("120s"); the input edits whole seconds.
  const [restartLoopWindowSeconds, setRestartLoopWindowSeconds] = useState(
    durationStringToSeconds(getConfigField(initialData?.config, "restartLoopWindow")),
  );
  const [dockerRestartLoopOpen, setDockerRestartLoopOpen] = useState(false);
  const [oid, setOid] = useState(getConfigField(initialData?.config, "oid"));
  const [community, setCommunity] = useState(getConfigField(initialData?.config, "community"));
  const [waitSelector, setWaitSelector] = useState(getConfigField(initialData?.config, "waitSelector"));
  const [keyword, setKeyword] = useState(getConfigField(initialData?.config, "keyword"));
  const [wsSend, setWsSend] = useState(getConfigField(initialData?.config, "send"));
  const [wsExpect, setWsExpect] = useState(getConfigField(initialData?.config, "expect"));
  const [expectedValue, setExpectedValue] = useState(getConfigField(initialData?.config, "expectedValue"));
  const [snmpOperator, setSnmpOperator] = useState(getConfigField(initialData?.config, "operator") || "equals");
  // freebox_line state — connectionUid + linkType + per-link-type thresholds.
  // Threshold fields are kept as strings to allow empty inputs (treated as "skip").
  const [freeboxConnectionUid, setFreeboxConnectionUid] = useState(
    getConfigField(initialData?.config, "connectionUid"),
  );
  const [freeboxLinkType, setFreeboxLinkType] = useState(
    getConfigField(initialData?.config, "linkType") || "xdsl",
  );
  const [freeboxMinSyncRate, setFreeboxMinSyncRate] = useState(
    getConfigField(initialData?.config, "minSyncRateDownKbps"),
  );
  const [freeboxMinSnrDb, setFreeboxMinSnrDb] = useState(
    getConfigField(initialData?.config, "minSnrMarginDownDb"),
  );
  const [freeboxMaxAttnDb, setFreeboxMaxAttnDb] = useState(
    getConfigField(initialData?.config, "maxAttenuationDb"),
  );
  const [freeboxMaxCrcErrors, setFreeboxMaxCrcErrors] = useState(
    getConfigField(initialData?.config, "maxCrcErrorsPerRun"),
  );
  const [freeboxMinRxMw, setFreeboxMinRxMw] = useState(
    getConfigField(initialData?.config, "minRxPowerMw"),
  );
  const [freeboxMaxRxMw, setFreeboxMaxRxMw] = useState(
    getConfigField(initialData?.config, "maxRxPowerMw"),
  );
  const [freeboxThresholdsOpen, setFreeboxThresholdsOpen] = useState(false);
  // dnsbl state — target IP/hostname + optional blocklist zones + optional resolver.
  // blocklists are stored as a comma/newline-separated string in the form and
  // split into an array on submit (empty = backend defaults).
  const [dnsblTarget, setDnsblTarget] = useState(getConfigField(initialData?.config, "target"));
  const [dnsblBlocklists, setDnsblBlocklists] = useState(
    Array.isArray(initialData?.config?.blocklists)
      ? (initialData.config.blocklists as string[]).join("\n")
      : getConfigField(initialData?.config, "blocklists"),
  );
  const [dnsblNameserver, setDnsblNameserver] = useState(
    getConfigField(initialData?.config, "nameserver"),
  );
  // dns state — the queried domain reuses the shared `host` state (backend reads
  // it under the `host` key). dnsNameserver is the optional custom resolver
  // (host:port), dnsRecordType the record to query (default A) so a sample's or
  // an existing check's record type is never silently discarded on save.
  const [dnsNameserver, setDnsNameserver] = useState(
    getConfigField(initialData?.config, "nameserver"),
  );
  const [dnsRecordType, setDnsRecordType] = useState(
    getConfigField(initialData?.config, "record_type") || "A",
  );
  // sip state — transport (udp/tcp/tls), mode (options/register), and an
  // optional comma-separated expect_status list. host/port/domain/username/
  // password reuse the shared state above; password is a secret field rendered
  // with a configPrivateKeys placeholder on edit.
  const [sipTransport, setSipTransport] = useState(
    getConfigField(initialData?.config, "transport") || "udp",
  );
  const [sipMode, setSipMode] = useState(
    getConfigField(initialData?.config, "mode") || "options",
  );
  const [sipExpectStatus, setSipExpectStatus] = useState(
    getConfigField(initialData?.config, "expect_status"),
  );
  // ICMP "Discover from Freebox" picker — opens a modal that lists hosts
  // currently seen by a paired Freebox so the user can pre-fill an ICMP
  // check without typing an IP.
  const [discoverOpen, setDiscoverOpen] = useState(false);
  // SSL expiry tiers. criticalDays (paging, StatusDown) reads the legacy
  // thresholdDays for back-compat; warningDays (amber, non-paging) is new.
  const [criticalDays, setCriticalDays] = useState(
    getConfigField(initialData?.config, "criticalDays") ||
      getConfigField(initialData?.config, "thresholdDays") ||
      getConfigField(initialData?.config, "threshold_days"),
  );
  const [warningDays, setWarningDays] = useState(
    getConfigField(initialData?.config, "warningDays") ||
      getConfigField(initialData?.config, "warning_days"),
  );
  const [serverName, setServerName] = useState(
    getConfigField(initialData?.config, "serverName") ||
      getConfigField(initialData?.config, "server_name"),
  );
  // ntp state — host/port reuse the shared inputs. Version (3/4) plus the
  // optional SSL-style offset warn/critical (ms) and max-stratum thresholds.
  const [ntpVersion, setNtpVersion] = useState(
    String(getConfigField(initialData?.config, "version") || "4"),
  );
  const [ntpOffsetWarnMs, setNtpOffsetWarnMs] = useState(
    getConfigField(initialData?.config, "offset_warn_ms"),
  );
  const [ntpOffsetCritMs, setNtpOffsetCritMs] = useState(
    getConfigField(initialData?.config, "offset_crit_ms"),
  );
  const [ntpMaxStratum, setNtpMaxStratum] = useState(
    getConfigField(initialData?.config, "max_stratum"),
  );
  // rdp state — host/port reuse the shared inputs. Require NLA enforces
  // CredSSP (Network Level Authentication) selection; the optional SSL-style
  // certificate warning/critical days grade the server certificate expiry
  // when a TLS-based protocol is negotiated (0/empty = off).
  const [rdpRequireNLA, setRdpRequireNLA] = useState(
    getConfigField(initialData?.config, "require_nla") === "true",
  );
  const [rdpWarningDays, setRdpWarningDays] = useState(
    getConfigField(initialData?.config, "warning_days"),
  );
  const [rdpCriticalDays, setRdpCriticalDays] = useState(
    getConfigField(initialData?.config, "critical_days"),
  );
  // sleep state — synthetic/testing checker, no network I/O. sleepStatus
  // mirrors the snmpOperator/sipMode pattern: state defaults to the backend's
  // implicit default ("up") and is only added to the submitted config when it
  // differs, rather than round-tripping an empty string through a Select.
  const [sleepMs, setSleepMs] = useState(getConfigField(initialData?.config, "sleep_ms"));
  const [jitterMs, setJitterMs] = useState(getConfigField(initialData?.config, "jitter_ms"));
  const [sleepStatus, setSleepStatus] = useState(
    getConfigField(initialData?.config, "status") || "up",
  );
  // secretHeaders: array of {key, value} rows for the HTTP secret headers form section
  const [secretHeaders, setSecretHeaders] = useState<{ key: string; value: string }[]>(() => {
    const raw = initialData?.config?.secretHeaders;
    if (raw && typeof raw === "object" && !Array.isArray(raw)) {
      return Object.entries(raw as Record<string, string>).map(([key, value]) => ({ key, value }));
    }
    return [];
  });
  const [selectedRegions, setSelectedRegions] = useState<string[]>(initialData?.regions ?? defaultRegions ?? []);
  const [reopenCooldownMultiplier, setReopenCooldownMultiplier] = useState(initialData?.reopenCooldownMultiplier?.toString() ?? "");
  const [flappingWindowSeconds, setFlappingWindowSeconds] = useState(initialData?.flappingWindowSeconds?.toString() ?? "");
  const [flapBackoffFactor, setFlapBackoffFactor] = useState(initialData?.flapBackoffFactor?.toString() ?? "");
  const [maxRecoveryMultiplier, setMaxRecoveryMultiplier] = useState(initialData?.maxRecoveryMultiplier?.toString() ?? "");
  const [confirmationPeriodSeconds, setConfirmationPeriodSeconds] = useState(
    initialData?.confirmationPeriodSeconds?.toString() ?? "",
  );
  const [recoveryPeriodSeconds, setRecoveryPeriodSeconds] = useState(
    initialData?.recoveryPeriodSeconds?.toString() ?? "",
  );
  const [error, setError] = useState<string | null>(null);
  // Bumped on every submit attempt; collapsible sections that own a live
  // validation error read it via expandSignal to force-expand + scroll.
  const [submitAttempts, setSubmitAttempts] = useState(0);

  // Check type combobox state
  const [typeSearchOpen, setTypeSearchOpen] = useState(false);
  const [typeSearch, setTypeSearch] = useState("");
  const searchInputRef = useRef<HTMLInputElement>(null);

  // Focus search input when popover opens
  useEffect(() => {
    if (typeSearchOpen) {
      setTimeout(() => searchInputRef.current?.focus(), 0);
    }
  }, [typeSearchOpen]);

  const filteredCheckTypes = useMemo(() => {
    if (!typeSearch) return availableCheckTypes;
    const q = typeSearch.toLowerCase();
    return availableCheckTypes.filter(
      (ct) => ct.label.toLowerCase().includes(q) || ct.description.toLowerCase().includes(q) || ct.value.includes(q)
    );
  }, [availableCheckTypes, typeSearch]);

  // Lazy-loaded samples for the currently selected type
  const { data: fetchedSamples, refetch: fetchSamples, isFetching: isFetchingSamples } = useSampleConfigs(type);
  const [samplePickerOpen, setSamplePickerOpen] = useState(false);

  // Close sample picker when type changes; the query key includes type, so the
  // next open will refetch automatically.
  useEffect(() => {
    setSamplePickerOpen(false);
  }, [type]);

  // `?section=<name>` deep-link: scroll the requested collapsible into view on
  // mount (it is also expanded via defaultOpen below). Deferred a tick so the
  // section is laid out first.
  useEffect(() => {
    if (!initialSection) return;
    const id = window.setTimeout(() => {
      document
        .getElementById(initialSection)
        ?.scrollIntoView({ behavior: "smooth", block: "start" });
    }, 100);
    return () => window.clearTimeout(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Interval options filtered by type constraints
  const intervalOptions = useMemo(() => {
    const { minSec, maxSec } = getPeriodConstraints(type);
    return buildIntervalOptions(minSec, maxSec);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [type, checkTypeInfoMap]);

  // Live polling interval (seconds) used by the Confirmation / Recovery estimate
  // lines. Active checks have a real cadence (the selected HMS interval); passive
  // checks (heartbeat / email) have no real probe cadence, so we pass 0 and the
  // estimate shows the duration only — never a probe count.
  const estimateIntervalSeconds = isPassiveType(type) ? 0 : hmsToSeconds(period);

  // Apply sample config to form
  function applySample(sample: SampleConfig) {
    setName(sample.name);
    setSlug(sample.slug);
    setPeriod(secondsToHMS(sample.periodSeconds));
    const cfg = sample.config;
    setUrl(getConfigField(cfg, "url"));
    setHost(getConfigField(cfg, "host"));
    setPort(getConfigField(cfg, "port"));
    setDomain(getConfigField(cfg, "domain"));
    setMethod(getConfigField(cfg, "method") || "GET");
    setExpectedStatus(getConfigField(cfg, "expectedStatus") || "200");
    setStartTLS(getConfigField(cfg, "starttls") === "true");
    setTlsVerify(getConfigField(cfg, "tls_verify") === "true");
    setEhloDomain(getConfigField(cfg, "ehlo_domain"));
    setExpectGreeting(getConfigField(cfg, "expect_greeting"));
    setCheckAuth(getConfigField(cfg, "check_auth") === "true");
    setUsername(getConfigField(cfg, "username"));
    setPassword(getConfigField(cfg, "password"));
    setDatabase(getConfigField(cfg, "database"));
    setQuery(getConfigField(cfg, "query"));
    setVhost(getConfigField(cfg, "vhost"));
    setQueue(getConfigField(cfg, "queue"));
    setScript(getConfigField(cfg, "script"));
    setServiceName(getConfigField(cfg, "serviceName"));
    setTls(getConfigField(cfg, "tls") === "true");
    setMinPlayers(getConfigField(cfg, "minPlayers"));
    setMaxPlayersField(getConfigField(cfg, "maxPlayers"));
    setBrokers(
      Array.isArray(cfg.brokers)
        ? (cfg.brokers as string[]).join(", ")
        : getConfigField(cfg, "brokers")
    );
    setTopic(getConfigField(cfg, "topic"));
    setProduceTest(getConfigField(cfg, "produceTest") === "true");
    setContainerName(getConfigField(cfg, "containerName"));
    setContainerId(getConfigField(cfg, "containerId"));
    setRestartLoopMinRestarts(getConfigField(cfg, "restartLoopMinRestarts"));
    setRestartLoopWindowSeconds(durationStringToSeconds(getConfigField(cfg, "restartLoopWindow")));
    setOid(getConfigField(cfg, "oid"));
    setCommunity(getConfigField(cfg, "community"));
    setWaitSelector(getConfigField(cfg, "waitSelector"));
    setKeyword(getConfigField(cfg, "keyword"));
    setExpectedValue(getConfigField(cfg, "expectedValue"));
    setSnmpOperator(getConfigField(cfg, "operator") || "equals");
    setWsSend(getConfigField(cfg, "send"));
    setWsExpect(getConfigField(cfg, "expect"));
    setDnsblTarget(getConfigField(cfg, "target"));
    setDnsblBlocklists(
      Array.isArray(cfg.blocklists)
        ? (cfg.blocklists as string[]).join("\n")
        : getConfigField(cfg, "blocklists"),
    );
    setDnsblNameserver(getConfigField(cfg, "nameserver"));
    // DNS samples carry the queried domain under `host` (already applied above
    // via setHost) plus an optional nameserver and a record_type.
    setDnsNameserver(getConfigField(cfg, "nameserver"));
    setDnsRecordType(getConfigField(cfg, "record_type") || "A");
    setSipTransport(getConfigField(cfg, "transport") || "udp");
    setSipMode(getConfigField(cfg, "mode") || "options");
    setSipExpectStatus(getConfigField(cfg, "expect_status"));
    setRdpRequireNLA(getConfigField(cfg, "require_nla") === "true");
    setRdpWarningDays(getConfigField(cfg, "warning_days"));
    setRdpCriticalDays(getConfigField(cfg, "critical_days"));
    setSleepMs(getConfigField(cfg, "sleep_ms"));
    setJitterMs(getConfigField(cfg, "jitter_ms"));
    setSleepStatus(getConfigField(cfg, "status") || "up");
    setTimeoutSeconds(durationStringToSeconds(getConfigField(cfg, "timeout")));
  }

  const currentConfig = useMemo(() => {
    const cfg: Record<string, unknown> = {};
    switch (type) {
      case "http":
        if (url) cfg.url = url;
        if (method && method !== "GET") cfg.method = method;
        {
          const statusCode = parseInt(expectedStatus, 10);
          if (!isNaN(statusCode) && statusCode !== 200) cfg.expectedStatus = statusCode;
        }
        if (username) cfg.username = username;
        if (password) cfg.password = password;
        {
          const shMap: Record<string, string> = {};
          for (const { key, value } of secretHeaders) {
            if (key) shMap[key] = value;
          }
          if (Object.keys(shMap).length > 0) cfg.secretHeaders = shMap;
        }
        break;
      case "websocket":
        if (url) cfg.url = url;
        if (wsSend) cfg.send = wsSend;
        if (wsExpect) cfg.expect = wsExpect;
        break;
      case "ssl":
        if (host) cfg.host = host;
        if (port) cfg.port = parseInt(port, 10);
        if (serverName) cfg.serverName = serverName;
        if (criticalDays) cfg.criticalDays = parseInt(criticalDays, 10);
        if (warningDays) cfg.warningDays = parseInt(warningDays, 10);
        break;
      case "ntp":
        if (host) cfg.host = host;
        if (port) cfg.port = parseInt(port, 10);
        if (ntpVersion) cfg.version = parseInt(ntpVersion, 10);
        if (ntpOffsetWarnMs) cfg.offset_warn_ms = parseInt(ntpOffsetWarnMs, 10);
        if (ntpOffsetCritMs) cfg.offset_crit_ms = parseInt(ntpOffsetCritMs, 10);
        if (ntpMaxStratum) cfg.max_stratum = parseInt(ntpMaxStratum, 10);
        break;
      case "rdp":
        if (host) cfg.host = host;
        if (port) cfg.port = parseInt(port, 10);
        if (rdpRequireNLA) cfg.require_nla = true;
        if (rdpWarningDays) cfg.warning_days = parseInt(rdpWarningDays, 10);
        if (rdpCriticalDays) cfg.critical_days = parseInt(rdpCriticalDays, 10);
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
      case "sftp":
        if (host) cfg.host = host;
        if (port) cfg.port = parseInt(port, 10);
        if (username) cfg.username = username;
        if (password) cfg.password = password;
        break;
      case "pop3":
      case "imap":
        if (host) cfg.host = host;
        if (port) cfg.port = parseInt(port, 10);
        if (tls) cfg.tls = true;
        if (startTLS) cfg.starttls = true;
        if (username) cfg.username = username;
        if (password) cfg.password = password;
        break;
      case "icmp":
        if (host) cfg.host = host;
        break;
      case "dns":
        // DNS queries a domain (backend key `host`), optionally via a custom
        // resolver, for a given record type. The visible label stays "Domain".
        if (host) cfg.host = host;
        if (dnsNameserver) cfg.nameserver = dnsNameserver;
        if (dnsRecordType && dnsRecordType !== "A") cfg.record_type = dnsRecordType;
        break;
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
      case "mysql":
      case "mssql":
      case "oracle":
        if (host) cfg.host = host;
        if (port) cfg.port = parseInt(port, 10);
        if (username) cfg.username = username;
        if (password) cfg.password = password;
        if (database) cfg.database = database;
        if (query) cfg.query = query;
        break;
      case "redis":
        if (host) cfg.host = host;
        if (port) cfg.port = parseInt(port, 10);
        if (password) cfg.password = password;
        if (database) cfg.database = parseInt(database, 10);
        break;
      case "mongodb":
        if (host) cfg.host = host;
        if (port) cfg.port = parseInt(port, 10);
        if (username) cfg.username = username;
        if (password) cfg.password = password;
        if (database) cfg.database = database;
        break;
      case "rabbitmq":
        if (host) cfg.host = host;
        if (port) cfg.port = parseInt(port, 10);
        if (username) cfg.username = username;
        if (password) cfg.password = password;
        if (vhost) cfg.vhost = vhost;
        if (queue) cfg.queue = queue;
        if (tlsVerify) cfg.tls = true;
        break;
      case "js":
        if (script) cfg.script = script;
        break;
      case "grpc":
        if (host) cfg.host = host;
        if (port) cfg.port = parseInt(port, 10);
        if (serviceName) cfg.serviceName = serviceName;
        if (tls) cfg.tls = true;
        break;
      case "kafka":
        if (brokers) cfg.brokers = brokers.split(",").map((b) => b.trim()).filter(Boolean);
        if (topic) cfg.topic = topic;
        if (username) cfg.saslUsername = username;
        if (password) cfg.saslPassword = password;
        if (username || password) cfg.saslMechanism = "PLAIN";
        if (tls) cfg.tls = true;
        if (produceTest) cfg.produceTest = true;
        break;
      case "mqtt":
        if (host) cfg.host = host;
        if (port) cfg.port = parseInt(port, 10);
        if (username) cfg.username = username;
        if (password) cfg.password = password;
        if (topic) cfg.topic = topic;
        if (tls) cfg.tls = true;
        break;
      case "a2s":
        if (host) cfg.host = host;
        if (port) cfg.port = parseInt(port, 10);
        if (minPlayers) cfg.minPlayers = parseInt(minPlayers, 10);
        if (maxPlayersField) cfg.maxPlayers = parseInt(maxPlayersField, 10);
        break;
      case "minecraft":
        if (host) cfg.host = host;
        if (port) cfg.port = parseInt(port, 10);
        if (edition && edition !== "java") cfg.edition = edition;
        if (minPlayers) cfg.minPlayers = parseInt(minPlayers, 10);
        if (maxPlayersField) cfg.maxPlayers = parseInt(maxPlayersField, 10);
        break;
      case "snmp":
        if (host) cfg.host = host;
        if (port) cfg.port = parseInt(port, 10);
        if (oid) cfg.oid = oid;
        if (community) cfg.community = community;
        if (expectedValue) cfg.expectedValue = expectedValue;
        if (snmpOperator && snmpOperator !== "equals") cfg.operator = snmpOperator;
        break;
      case "docker":
        if (containerName) cfg.containerName = containerName;
        if (containerId) cfg.containerId = containerId;
        if (host) cfg.host = host;
        if (restartLoopMinRestarts) cfg.restartLoopMinRestarts = parseInt(restartLoopMinRestarts, 10);
        if (restartLoopWindowSeconds) cfg.restartLoopWindow = `${parseInt(restartLoopWindowSeconds, 10)}s`;
        break;
      case "browser":
        if (url) cfg.url = url;
        if (waitSelector) cfg.waitSelector = waitSelector;
        if (keyword) cfg.keyword = keyword;
        break;
      case "freebox_line":
        if (freeboxConnectionUid) cfg.connectionUid = freeboxConnectionUid;
        if (freeboxLinkType) cfg.linkType = freeboxLinkType;
        if (freeboxMinSyncRate) cfg.minSyncRateDownKbps = parseInt(freeboxMinSyncRate, 10);
        if (freeboxMinSnrDb) cfg.minSnrMarginDownDb = parseInt(freeboxMinSnrDb, 10);
        if (freeboxMaxAttnDb) cfg.maxAttenuationDb = parseInt(freeboxMaxAttnDb, 10);
        if (freeboxMaxCrcErrors) cfg.maxCrcErrorsPerRun = parseInt(freeboxMaxCrcErrors, 10);
        if (freeboxMinRxMw) cfg.minRxPowerMw = parseFloat(freeboxMinRxMw);
        if (freeboxMaxRxMw) cfg.maxRxPowerMw = parseFloat(freeboxMaxRxMw);
        break;
      case "dnsbl":
        if (dnsblTarget) cfg.target = dnsblTarget;
        {
          const zones = splitBlocklists(dnsblBlocklists);
          if (zones.length > 0) cfg.blocklists = zones;
        }
        if (dnsblNameserver) cfg.nameserver = dnsblNameserver;
        break;
      case "sip":
        if (host) cfg.host = host;
        if (port) cfg.port = parseInt(port, 10);
        if (sipTransport && sipTransport !== "udp") cfg.transport = sipTransport;
        if (sipMode && sipMode !== "options") cfg.mode = sipMode;
        if (domain) cfg.domain = domain;
        if (sipMode === "register") {
          if (username) cfg.username = username;
          if (password) cfg.password = password;
        }
        if (sipMode === "options" && sipExpectStatus) cfg.expect_status = sipExpectStatus;
        break;
      case "sleep":
        if (sleepMs) cfg.sleep_ms = parseInt(sleepMs, 10);
        if (jitterMs) cfg.jitter_ms = parseInt(jitterMs, 10);
        if (sleepStatus && sleepStatus !== "up") cfg.status = sleepStatus;
        break;
    }
    // Shared optional per-check timeout ("Ns" duration string) — passive
    // types never probe, so the key is meaningless there.
    if (!isPassiveType(type) && timeoutSeconds !== "") {
      const t = parseInt(timeoutSeconds, 10);
      if (!isNaN(t)) cfg.timeout = `${t}s`;
    }
    return cfg;
  }, [type, timeoutSeconds, url, host, port, domain, method, expectedStatus, username, password, secretHeaders,
    startTLS, tlsVerify, ehloDomain, expectGreeting, checkAuth, database, query, script,
    serviceName, tls, brokers, topic, produceTest, minPlayers, maxPlayersField, edition,
    vhost, queue, oid, community, expectedValue, snmpOperator, containerName, containerId,
    restartLoopMinRestarts, restartLoopWindowSeconds,
    waitSelector, keyword, wsSend, wsExpect, serverName, criticalDays, warningDays,
    freeboxConnectionUid, freeboxLinkType, freeboxMinSyncRate, freeboxMinSnrDb,
    freeboxMaxAttnDb, freeboxMaxCrcErrors, freeboxMinRxMw, freeboxMaxRxMw,
    dnsblTarget, dnsblBlocklists, dnsblNameserver,
    dnsNameserver, dnsRecordType,
    sipTransport, sipMode, sipExpectStatus,
    ntpVersion, ntpOffsetWarnMs, ntpOffsetCritMs, ntpMaxStratum,
    rdpRequireNLA, rdpWarningDays, rdpCriticalDays,
    sleepMs, jitterMs, sleepStatus]);

  const fieldErrors = useCheckValidation(org, type, currentConfig, 300);

  const toggleRegion = (slug: string) => {
    setSelectedRegions((prev) =>
      prev.includes(slug) ? prev.filter((r) => r !== slug) : [...prev, slug]
    );
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSubmitAttempts((n) => n + 1);

    // Serialize ONCE: reuse `currentConfig` (the same object the live preview
    // and validation use) as the submitted payload, so preview and payload can
    // never drift apart. Submit-only concerns are layered on top.
    const config: Record<string, unknown> = { ...currentConfig };
    // HTTP clears previously-stored secret headers on edit by sending an
    // explicit empty object; `currentConfig` omits the key when empty.
    if (type === "http" && config.secretHeaders === undefined) {
      config.secretHeaders = {};
    }
    const requiredError = requiredFieldError(
      type,
      config,
      initialData?.configPrivateKeys,
    );
    if (requiredError) {
      setError(requiredError);
      return;
    }

    // Optional per-check timeout — written as a "Ns" duration string when
    // set; an empty field omits the key entirely so clearing it on edit
    // removes it from config (the server caps at 30s and stays
    // authoritative).
    if (!isPassiveType(type) && timeoutSeconds !== "") {
      const timeoutValue = parseInt(timeoutSeconds, 10);
      if (isNaN(timeoutValue) || timeoutValue < 1 || timeoutValue > 30) {
        setError("Timeout must be between 1 and 30 seconds");
        return;
      }
      config.timeout = `${timeoutValue}s`;
    }

    // Validate slug format
    if (slugError) {
      setError(slugError);
      return;
    }

    // Validate period against constraints
    const periodSec = hmsToSeconds(isPassiveType(type) ? formatPeriod(periodValue, periodUnit) : period);
    const { minSec, maxSec } = getPeriodConstraints(type);
    if (periodSec < minSec) {
      setError(`Minimum check interval for ${type} is ${secondsToHMS(minSec)}`);
      return;
    }
    if (maxSec > 0 && periodSec > maxSec) {
      setError(`Maximum check interval for ${type} is ${secondsToHMS(maxSec)}`);
      return;
    }

    try {
      await onSubmit({
        type: mode === "create" ? type : undefined,
        enabled,
        name: mode === "edit" ? name : (name || undefined),
        slug: mode === "edit" ? slug : (slug || undefined),
        checkGroupUid: checkGroupUid || (mode === "edit" ? "" : undefined),
        period: isPassiveType(type) ? formatPeriod(periodValue, periodUnit) : period,
        // Don't send config for passive edits — the token is managed by the backend
        ...(isPassiveType(type) && mode === "edit" ? {} : { config }),
        ...(showRegions ? { regions: selectedRegions } : {}),
        reopenCooldownMultiplier: reopenCooldownMultiplier !== "" ? parseInt(reopenCooldownMultiplier, 10) : null,
        ...(flappingWindowSeconds !== "" ? { flappingWindowSeconds: parseInt(flappingWindowSeconds, 10) } : {}),
        ...(flapBackoffFactor !== "" ? { flapBackoffFactor: parseInt(flapBackoffFactor, 10) } : {}),
        ...(maxRecoveryMultiplier !== "" ? { maxRecoveryMultiplier: parseInt(maxRecoveryMultiplier, 10) } : {}),
        ...(confirmationPeriodSeconds !== ""
          ? { confirmationPeriodSeconds: parseInt(confirmationPeriodSeconds, 10) }
          : {}),
        ...(recoveryPeriodSeconds !== ""
          ? { recoveryPeriodSeconds: parseInt(recoveryPeriodSeconds, 10) }
          : {}),
        ...(mode === "create" || labelsDirty ? { labels } : {}),
        ...(connectionUids !== null ? { connectionUids } : {}),
        ...(dependsOnParents !== null
          ? {
              dependsOnParentUids: dependsOnParents.map((p) => p.uid),
              initialDependsOnParentUids: initialParentUids,
            }
          : {}),
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
            <div className="space-y-2">
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
            <div className="flex gap-4">
              <div className="space-y-2 flex-1">
                <Label htmlFor="username">Username (optional, Basic Auth)</Label>
                <Input id="username" type="text" placeholder="user" value={username} onChange={(e) => setUsername(e.target.value)} data-testid="check-username-input" />
              </div>
              <div className="space-y-2 flex-1">
                <Label htmlFor="password">Password (optional)</Label>
                <Input id="password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} data-testid="check-password-input" />
              </div>
            </div>
            <div className="space-y-2">
              <div>
                <Label>{t("secretHeaders")}</Label>
                <p className="text-xs text-muted-foreground mt-0.5">{t("secretHeadersDescription")}</p>
              </div>
              {initialData?.configPrivateKeys?.includes("secretHeaders") && secretHeaders.length === 0 && (
                <p className="text-xs text-muted-foreground">
                  <span className="font-mono tracking-widest">••••</span>
                  {" "}
                  <span className="italic">(encrypted — enter new values to replace)</span>
                </p>
              )}
              {secretHeaders.map((row, idx) => (
                <div key={idx} className="flex gap-2 items-center">
                  <Input
                    type="text"
                    placeholder="Header-Name"
                    value={row.key}
                    onChange={(e) => {
                      const updated = [...secretHeaders];
                      updated[idx] = { ...updated[idx], key: e.target.value };
                      setSecretHeaders(updated);
                    }}
                    className="flex-1"
                    data-testid={`secret-header-key-${idx}`}
                  />
                  <Input
                    type="password"
                    placeholder="value"
                    value={row.value}
                    onChange={(e) => {
                      const updated = [...secretHeaders];
                      updated[idx] = { ...updated[idx], value: e.target.value };
                      setSecretHeaders(updated);
                    }}
                    className="flex-1"
                    data-testid={`secret-header-value-${idx}`}
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    className="text-destructive shrink-0"
                    onClick={() => setSecretHeaders(secretHeaders.filter((_, i) => i !== idx))}
                    data-testid={`secret-header-remove-${idx}`}
                  >
                    <Trash2 className="h-4 w-4" />
                  </Button>
                </div>
              ))}
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setSecretHeaders([...secretHeaders, { key: "", value: "" }])}
                data-testid="add-secret-header-button"
              >
                <Plus className="h-4 w-4 mr-1" />
                {t("addSecretHeader")}
              </Button>
            </div>
          </>
        );
      case "ssl":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input id="host" type="text" placeholder="example.com" value={host} onChange={(e) => setHost(e.target.value)}
                  className={cn("flex-1", getFieldError(fieldErrors, "host") && "border-destructive")} data-testid="check-host-input" />
                <Input id="port" type="number" placeholder="443" value={port} onChange={(e) => setPort(e.target.value)}
                  className={cn("w-24", getFieldError(fieldErrors, "port") && "border-destructive")} data-testid="check-port-input" />
              </div>
              {getFieldError(fieldErrors, "host") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "host")}</p>)}
              {getFieldError(fieldErrors, "port") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "port")}</p>)}
            </div>
            <div className="space-y-2">
              <Label htmlFor="serverName">Server Name (SNI, optional)</Label>
              <Input id="serverName" type="text" placeholder="defaults to host" value={serverName} onChange={(e) => setServerName(e.target.value)} data-testid="check-server-name-input" />
            </div>
            <div className="flex gap-4">
              <div className="space-y-2 w-40">
                <Label htmlFor="criticalDays">Critical (days)</Label>
                <Input id="criticalDays" type="number" placeholder="30" value={criticalDays} onChange={(e) => setCriticalDays(e.target.value)} data-testid="check-critical-days-input" />
                <p className="text-xs text-muted-foreground">Down (pages) at or below this.</p>
              </div>
              <div className="space-y-2 w-40">
                <Label htmlFor="warningDays">Warning (days)</Label>
                <Input id="warningDays" type="number" placeholder="30" value={warningDays} onChange={(e) => setWarningDays(e.target.value)} data-testid="check-warning-days-input" />
                <p className="text-xs text-muted-foreground">Amber warning (no page) at or below this. Must be ≥ Critical.</p>
              </div>
            </div>
          </>
        );
      case "ntp":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input id="host" type="text" placeholder="pool.ntp.org" value={host} onChange={(e) => setHost(e.target.value)}
                  className={cn("flex-1", getFieldError(fieldErrors, "host") && "border-destructive")} data-testid="check-host-input" />
                <Input id="port" type="number" placeholder="123" value={port} onChange={(e) => setPort(e.target.value)}
                  className={cn("w-24", getFieldError(fieldErrors, "port") && "border-destructive")} data-testid="check-port-input" />
              </div>
              {getFieldError(fieldErrors, "host") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "host")}</p>)}
              {getFieldError(fieldErrors, "port") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "port")}</p>)}
            </div>
            <div className="space-y-2 w-40">
              <Label htmlFor="ntpVersion">Version</Label>
              <Select value={ntpVersion} onValueChange={setNtpVersion}>
                <SelectTrigger data-testid="check-ntp-version-select"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="4">4</SelectItem>
                  <SelectItem value="3">3</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="flex gap-4">
              <div className="space-y-2 w-40">
                <Label htmlFor="ntpOffsetCritMs">Offset critical (ms)</Label>
                <Input id="ntpOffsetCritMs" type="number" min={0} placeholder="off" value={ntpOffsetCritMs} onChange={(e) => setNtpOffsetCritMs(e.target.value)} data-testid="check-ntp-offset-crit-input" />
                <p className="text-xs text-muted-foreground">Down (pages) when |offset| exceeds this. Worker-relative.</p>
              </div>
              <div className="space-y-2 w-40">
                <Label htmlFor="ntpOffsetWarnMs">Offset warning (ms)</Label>
                <Input id="ntpOffsetWarnMs" type="number" min={0} placeholder="off" value={ntpOffsetWarnMs} onChange={(e) => setNtpOffsetWarnMs(e.target.value)} data-testid="check-ntp-offset-warn-input" />
                <p className="text-xs text-muted-foreground">Amber (no page) when |offset| exceeds this. Must be ≤ Critical.</p>
              </div>
            </div>
            <div className="space-y-2 w-40">
              <Label htmlFor="ntpMaxStratum">Max stratum (optional)</Label>
              <Input id="ntpMaxStratum" type="number" min={1} max={15} placeholder="off" value={ntpMaxStratum} onChange={(e) => setNtpMaxStratum(e.target.value)} data-testid="check-ntp-max-stratum-input" />
              <p className="text-xs text-muted-foreground">Down when the server's stratum exceeds this (1–15).</p>
            </div>
          </>
        );
      case "rdp":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input id="host" type="text" placeholder="rdp.example.internal" value={host} onChange={(e) => setHost(e.target.value)}
                  className={cn("flex-1", getFieldError(fieldErrors, "host") && "border-destructive")} data-testid="check-host-input" />
                <Input id="port" type="number" placeholder="3389" value={port} onChange={(e) => setPort(e.target.value)}
                  className={cn("w-24", getFieldError(fieldErrors, "port") && "border-destructive")} data-testid="check-port-input" />
              </div>
              {getFieldError(fieldErrors, "host") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "host")}</p>)}
              {getFieldError(fieldErrors, "port") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "port")}</p>)}
            </div>
            <div className="space-y-2">
              <label className="flex items-center gap-2"><Checkbox checked={rdpRequireNLA} onCheckedChange={(v) => setRdpRequireNLA(v === true)} data-testid="check-rdp-require-nla-checkbox" /><span className="text-sm">Require NLA (Network Level Authentication)</span></label>
              <p className="text-xs text-muted-foreground">Down when the server does not select CredSSP — catches NLA silently disabled by policy.</p>
            </div>
            <div className="flex gap-4">
              <div className="space-y-2 w-40">
                <Label htmlFor="rdpCriticalDays">Cert critical (days)</Label>
                <Input id="rdpCriticalDays" type="number" min={0} placeholder="off" value={rdpCriticalDays} onChange={(e) => setRdpCriticalDays(e.target.value)} data-testid="check-rdp-critical-days-input" />
                <p className="text-xs text-muted-foreground">Down (pages) when the certificate expires in at most this many days.</p>
              </div>
              <div className="space-y-2 w-40">
                <Label htmlFor="rdpWarningDays">Cert warning (days)</Label>
                <Input id="rdpWarningDays" type="number" min={0} placeholder="off" value={rdpWarningDays} onChange={(e) => setRdpWarningDays(e.target.value)} data-testid="check-rdp-warning-days-input" />
                <p className="text-xs text-muted-foreground">Amber warning (no page). Must be ≥ Critical.</p>
              </div>
            </div>
            <p className="text-xs text-muted-foreground">Pre-auth handshake only — no credentials are sent. Workers need network access to the RDP host (typically internal).</p>
          </>
        );
      case "websocket":
        return (
          <>
            <div className="space-y-2">
              <Label htmlFor="url">URL</Label>
              <Input id="url" type="url" placeholder="wss://example.com/ws" value={url} onChange={(e) => setUrl(e.target.value)}
                className={cn(getFieldError(fieldErrors, "url") && "border-destructive")} data-testid="check-url-input" />
              {getFieldError(fieldErrors, "url") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "url")}</p>)}
            </div>
            <div className="space-y-2">
              <Label htmlFor="wsSend">Send (optional)</Label>
              <Input id="wsSend" type="text" placeholder="hello" value={wsSend} onChange={(e) => setWsSend(e.target.value)}
                data-testid="check-ws-send-input" />
            </div>
            <div className="space-y-2">
              <Label htmlFor="wsExpect">Expected pattern (regex, optional)</Label>
              <Input id="wsExpect" type="text" placeholder="hello" value={wsExpect} onChange={(e) => setWsExpect(e.target.value)}
                data-testid="check-ws-expect-input" />
            </div>
          </>
        );
      case "tcp":
      case "udp":
        return (
          <div className="space-y-2">
            <Label>Host</Label>
            <div className="flex gap-2">
              <Input id="host" type="text" placeholder={type === "udp" ? "8.8.8.8" : "example.com"} value={host} onChange={(e) => setHost(e.target.value)}
                className={cn("flex-1", getFieldError(fieldErrors, "host") && "border-destructive")} data-testid="check-host-input" />
              <Input id="port" type="number" placeholder={type === "udp" ? "53" : "443"} value={port} onChange={(e) => setPort(e.target.value)}
                className={cn("w-24", getFieldError(fieldErrors, "port") && "border-destructive")} data-testid="check-port-input" />
            </div>
            {getFieldError(fieldErrors, "host") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "host")}</p>)}
            {getFieldError(fieldErrors, "port") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "port")}</p>)}
          </div>
        );
      case "ssh":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input id="host" type="text" placeholder="server.example.com" value={host} onChange={(e) => setHost(e.target.value)}
                  className={cn("flex-1", getFieldError(fieldErrors, "host") && "border-destructive")} data-testid="check-host-input" />
                <Input id="port" type="number" placeholder="22" value={port} onChange={(e) => setPort(e.target.value)}
                  className={cn("w-24", getFieldError(fieldErrors, "port") && "border-destructive")} data-testid="check-port-input" />
              </div>
              {getFieldError(fieldErrors, "host") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "host")}</p>)}
              {getFieldError(fieldErrors, "port") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "port")}</p>)}
            </div>
            <div className="space-y-2">
              <Label htmlFor="username">Username (optional)</Label>
              <Input id="username" type="text" placeholder="user" value={username} onChange={(e) => setUsername(e.target.value)} data-testid="check-username-input" />
            </div>
            <div className="space-y-2">
              <Label htmlFor="password">Password (optional)</Label>
              <Input id="password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} data-testid="check-password-input" />
            </div>
          </>
        );
      case "pop3":
      case "imap": {
        // D2: port <-> TLS auto-toggle affordance. Selecting the implicit-TLS
        // well-known port (993 IMAP / 995 POP3) auto-checks the TLS toggle;
        // unchecking TLS restores the plaintext port as the placeholder
        // (never forced into the field, so it never fights a typed value).
        // Purely client-side guidance — the server derives independently
        // (checkimap/checkpop3 newExecParams) regardless of what the client
        // sends.
        const implicitTLSPort = type === "pop3" ? "995" : "993";
        const plaintextPort = type === "pop3" ? "110" : "143";
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input id="host" type="text" placeholder="mail.example.com" value={host} onChange={(e) => setHost(e.target.value)}
                  className={cn("flex-1", getFieldError(fieldErrors, "host") && "border-destructive")} data-testid="check-host-input" />
                <Input id="port" type="number" placeholder={tls ? implicitTLSPort : plaintextPort} value={port}
                  onChange={(e) => {
                    const value = e.target.value;
                    setPort(value);
                    if (value === implicitTLSPort && !startTLS) setTls(true);
                  }}
                  className={cn("w-24", getFieldError(fieldErrors, "port") && "border-destructive")} data-testid="check-port-input" />
              </div>
              {getFieldError(fieldErrors, "host") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "host")}</p>)}
              {getFieldError(fieldErrors, "port") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "port")}</p>)}
            </div>
            <div className="space-y-2">
              <label className="flex items-center gap-2">
                <Checkbox checked={tls} onCheckedChange={(v) => setTls(v === true)} data-testid="check-tls-checkbox" />
                <span className="text-sm">Use implicit TLS</span>
              </label>
              {(tls || port === implicitTLSPort) && (
                <p className="text-xs text-muted-foreground">
                  Port {implicitTLSPort} uses implicit TLS.
                </p>
              )}
              <label className="flex items-center gap-2">
                <Checkbox checked={startTLS} onCheckedChange={(v) => setStartTLS(v === true)} data-testid="check-starttls-checkbox" />
                <span className="text-sm">Use STARTTLS</span>
              </label>
            </div>
            <div className="space-y-2">
              <Label htmlFor="username">Username (optional)</Label>
              <Input id="username" type="text" placeholder="user" value={username} onChange={(e) => setUsername(e.target.value)} data-testid="check-username-input" />
            </div>
            <div className="space-y-2">
              <Label htmlFor="password">Password (optional)</Label>
              <Input id="password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} data-testid="check-password-input" />
            </div>
          </>
        );
      }
      case "ftp":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input id="host" type="text" placeholder="ftp.example.com" value={host} onChange={(e) => setHost(e.target.value)} className="flex-1" data-testid="check-host-input" />
                <Input id="port" type="number" placeholder="21" value={port} onChange={(e) => setPort(e.target.value)} className="w-24" data-testid="check-port-input" />
              </div>
            </div>
            <div className="space-y-2">
              <Label htmlFor="username">Username (optional, default: anonymous)</Label>
              <Input id="username" type="text" placeholder="anonymous" value={username} onChange={(e) => setUsername(e.target.value)} data-testid="check-username-input" />
            </div>
            <div className="space-y-2">
              <Label htmlFor="password">Password (optional)</Label>
              <Input id="password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} data-testid="check-password-input" />
            </div>
          </>
        );
      case "sftp":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input id="host" type="text" placeholder="sftp.example.com" value={host} onChange={(e) => setHost(e.target.value)} className="flex-1" data-testid="check-host-input" />
                <Input id="port" type="number" placeholder="22" value={port} onChange={(e) => setPort(e.target.value)} className="w-24" data-testid="check-port-input" />
              </div>
            </div>
            <div className="space-y-2">
              <Label htmlFor="username">Username</Label>
              <Input id="username" type="text" placeholder="user" value={username} onChange={(e) => setUsername(e.target.value)} data-testid="check-username-input" />
            </div>
            <div className="space-y-2">
              <Label htmlFor="password">Password</Label>
              <Input id="password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} data-testid="check-password-input" />
            </div>
          </>
        );
      case "icmp": {
        // Source integrations (canSource) drive the LAN-host discovery picker.
        // Today this is freebox only; the `granted` gate is the Freebox pairing
        // state. Filtering by capability keeps future source types eligible.
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
            <Input id="host" type="text" placeholder="example.com" value={host} onChange={(e) => setHost(e.target.value)}
              className={cn(getFieldError(fieldErrors, "host") && "border-destructive")} data-testid="check-host-input" />
            {getFieldError(fieldErrors, "host") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "host")}</p>)}
            {freeboxChannels.length > 0 && (
              <FreeboxLanDiscovery
                org={org}
                open={discoverOpen}
                onOpenChange={setDiscoverOpen}
                channels={freeboxChannels}
                onSelect={(picked: FreeboxLanHost) => {
                  setHost(picked.ip);
                  if (!name) {
                    setName(picked.name);
                  }
                }}
              />
            )}
          </div>
        );
      }
      case "dns":
        // The queried domain is bound to the shared `host` state (backend key
        // `host`); its validation error is keyed `host` too, so it renders here.
        return (
          <>
            <div className="space-y-2">
              <Label htmlFor="domain">Domain</Label>
              <Input id="domain" type="text" placeholder="example.com" value={host} onChange={(e) => setHost(e.target.value)}
                className={cn(getFieldError(fieldErrors, "host") && "border-destructive")} data-testid="check-domain-input" />
              {getFieldError(fieldErrors, "host") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "host")}</p>)}
            </div>
            <div className="space-y-2">
              <Label htmlFor="dnsRecordType">Record type</Label>
              <Select value={dnsRecordType} onValueChange={setDnsRecordType}>
                <SelectTrigger id="dnsRecordType" data-testid="check-dns-record-type-select"><SelectValue /></SelectTrigger>
                <SelectContent>
                  {["A", "AAAA", "CNAME", "MX", "NS", "TXT"].map((rt) => (
                    <SelectItem key={rt} value={rt}>{rt}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label htmlFor="dnsNameserver">DNS server (optional)</Label>
              <Input id="dnsNameserver" type="text" placeholder="8.8.8.8:53 — defaults to system resolver" value={dnsNameserver}
                onChange={(e) => setDnsNameserver(e.target.value)}
                className={cn(getFieldError(fieldErrors, "nameserver") && "border-destructive")}
                data-testid="check-dns-nameserver-input" />
              <p className="text-xs text-muted-foreground">Resolver to query, in host:port form. Leave blank to use the system resolver.</p>
              {getFieldError(fieldErrors, "nameserver") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "nameserver")}</p>)}
            </div>
          </>
        );
      case "domain":
        return (
          <div className="space-y-2">
            <Label htmlFor="domain">Domain</Label>
            <Input id="domain" type="text" placeholder="example.com" value={domain} onChange={(e) => setDomain(e.target.value)}
              className={cn(getFieldError(fieldErrors, "domain") && "border-destructive")} data-testid="check-domain-input" />
            {getFieldError(fieldErrors, "domain") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "domain")}</p>)}
          </div>
        );
      case "smtp":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input id="host" type="text" placeholder="mail.example.com" value={host} onChange={(e) => setHost(e.target.value)} className="flex-1" data-testid="check-host-input" />
                <Input id="port" type="number" placeholder="25" value={port} onChange={(e) => setPort(e.target.value)} className="w-24" data-testid="check-port-input" />
              </div>
            </div>
            <div className="space-y-3">
              <label className="flex items-center gap-2"><Checkbox checked={startTLS} onCheckedChange={(v) => setStartTLS(v === true)} data-testid="check-starttls-checkbox" /><span className="text-sm">Use STARTTLS</span></label>
              <label className="flex items-center gap-2"><Checkbox checked={tlsVerify} onCheckedChange={(v) => setTlsVerify(v === true)} data-testid="check-tls-verify-checkbox" /><span className="text-sm">Verify TLS certificate</span></label>
              <label className="flex items-center gap-2"><Checkbox checked={checkAuth} onCheckedChange={(v) => setCheckAuth(v === true)} data-testid="check-auth-checkbox" /><span className="text-sm">Check AUTH support</span></label>
            </div>
            <div className="space-y-2">
              <Label htmlFor="ehloDomain">EHLO Domain (optional)</Label>
              <Input id="ehloDomain" type="text" placeholder="example.com" value={ehloDomain} onChange={(e) => setEhloDomain(e.target.value)} data-testid="check-ehlo-domain-input" />
            </div>
            <div className="space-y-2">
              <Label htmlFor="expectGreeting">Expected Greeting (optional)</Label>
              <Input id="expectGreeting" type="text" placeholder="220" value={expectGreeting} onChange={(e) => setExpectGreeting(e.target.value)} data-testid="check-expect-greeting-input" />
            </div>
            <div className="flex gap-4">
              <div className="space-y-2 flex-1">
                <Label htmlFor="username">Username (optional, AUTH PLAIN)</Label>
                <Input id="username" type="text" placeholder="user@example.com" value={username} onChange={(e) => setUsername(e.target.value)} data-testid="check-username-input" />
              </div>
              <div className="space-y-2 flex-1">
                <Label htmlFor="password">Password (optional)</Label>
                <Input id="password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} data-testid="check-password-input" />
              </div>
            </div>
          </>
        );
      case "postgresql":
      case "mysql":
      case "mssql":
      case "oracle":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input id="host" type="text" placeholder="db.example.com" value={host} onChange={(e) => setHost(e.target.value)} className="flex-1" data-testid="check-host-input" />
                <Input id="port" type="number" placeholder={type === "mysql" ? "3306" : "5432"} value={port} onChange={(e) => setPort(e.target.value)} className="w-24" data-testid="check-port-input" />
              </div>
            </div>
            <div className="space-y-2"><Label htmlFor="username">Username</Label><Input id="username" type="text" placeholder={type === "mysql" ? "root" : "postgres"} value={username} onChange={(e) => setUsername(e.target.value)} data-testid="check-username-input" /></div>
            <div className="space-y-2"><Label htmlFor="password">Password (optional)</Label><Input id="password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} data-testid="check-password-input" /></div>
            <div className="space-y-2"><Label htmlFor="database">Database (optional)</Label><Input id="database" type="text" placeholder={type === "mysql" ? "mysql" : "postgres"} value={database} onChange={(e) => setDatabase(e.target.value)} data-testid="check-database-input" /></div>
            <div className="space-y-2"><Label htmlFor="query">Query (optional)</Label><Input id="query" type="text" placeholder="SELECT 1" value={query} onChange={(e) => setQuery(e.target.value)} data-testid="check-query-input" /></div>
          </>
        );
      case "redis":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input id="host" type="text" placeholder="redis.example.com" value={host} onChange={(e) => setHost(e.target.value)} className="flex-1" data-testid="check-host-input" />
                <Input id="port" type="number" placeholder="6379" value={port} onChange={(e) => setPort(e.target.value)} className="w-24" data-testid="check-port-input" />
              </div>
            </div>
            <div className="space-y-2"><Label htmlFor="password">Password (optional)</Label><Input id="password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} data-testid="check-password-input" /></div>
            <div className="space-y-2"><Label htmlFor="database">Database (optional, 0-15)</Label><Input id="database" type="number" placeholder="0" min={0} max={15} value={database} onChange={(e) => setDatabase(e.target.value)} data-testid="check-database-input" /></div>
          </>
        );
      case "mongodb":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input id="host" type="text" placeholder="mongo.example.com" value={host} onChange={(e) => setHost(e.target.value)} className="flex-1" data-testid="check-host-input" />
                <Input id="port" type="number" placeholder="27017" value={port} onChange={(e) => setPort(e.target.value)} className="w-24" data-testid="check-port-input" />
              </div>
            </div>
            <div className="space-y-2"><Label htmlFor="username">Username (optional)</Label><Input id="username" type="text" placeholder="admin" value={username} onChange={(e) => setUsername(e.target.value)} data-testid="check-username-input" /></div>
            <div className="space-y-2"><Label htmlFor="password">Password (optional)</Label><Input id="password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} data-testid="check-password-input" /></div>
            <div className="space-y-2"><Label htmlFor="database">Database (optional)</Label><Input id="database" type="text" placeholder="admin" value={database} onChange={(e) => setDatabase(e.target.value)} data-testid="check-database-input" /></div>
          </>
        );
      case "rabbitmq":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input id="host" type="text" placeholder="rabbitmq.example.com" value={host} onChange={(e) => setHost(e.target.value)} className="flex-1" data-testid="check-host-input" />
                <Input id="port" type="number" placeholder="5672" value={port} onChange={(e) => setPort(e.target.value)} className="w-24" data-testid="check-port-input" />
              </div>
            </div>
            <div className="space-y-2"><Label htmlFor="username">Username</Label><Input id="username" type="text" placeholder="guest" value={username} onChange={(e) => setUsername(e.target.value)} data-testid="check-username-input" /></div>
            <div className="space-y-2"><Label htmlFor="password">Password (optional)</Label><Input id="password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} data-testid="check-password-input" /></div>
            <div className="space-y-2"><Label htmlFor="vhost">Virtual Host (optional)</Label><Input id="vhost" type="text" placeholder="/" value={vhost} onChange={(e) => setVhost(e.target.value)} data-testid="check-vhost-input" /></div>
            <div className="space-y-2"><Label htmlFor="queue">Queue (optional)</Label><Input id="queue" type="text" placeholder="my-queue" value={queue} onChange={(e) => setQueue(e.target.value)} data-testid="check-queue-input" /></div>
            <div className="space-y-3">
              <label className="flex items-center gap-2"><Checkbox checked={tlsVerify} onCheckedChange={(v) => setTlsVerify(v === true)} data-testid="check-tls-checkbox" /><span className="text-sm">Use TLS</span></label>
            </div>
          </>
        );
      case "js":
        return (
          <div className="space-y-2">
            <Label htmlFor="script">Script</Label>
            <CodeMirror value={script} onChange={(value) => setScript(value)} extensions={[javascript()]}
              theme={document.documentElement.classList.contains("dark") ? "dark" : "light"} height="200px"
              className={cn("rounded-md border text-sm", getFieldError(fieldErrors, "script") && "border-destructive")} data-testid="check-script" />
            {getFieldError(fieldErrors, "script") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "script")}</p>)}
            <p className="text-xs text-muted-foreground">JavaScript script that returns an object with status (&quot;up&quot;, &quot;down&quot;, or &quot;error&quot;), optional metrics, and optional output.</p>
          </div>
        );
      case "grpc":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input id="host" type="text" placeholder="grpc.example.com" value={host} onChange={(e) => setHost(e.target.value)} className="flex-1" />
                <Input id="port" type="number" placeholder="50051" value={port} onChange={(e) => setPort(e.target.value)} className="w-24" />
              </div>
            </div>
            <div className="space-y-2">
              <Label htmlFor="serviceName">Service Name (optional)</Label>
              <Input id="serviceName" type="text" placeholder="myservice" value={serviceName} onChange={(e) => setServiceName(e.target.value)} />
              <p className="text-xs text-muted-foreground">Leave empty to check overall server health</p>
            </div>
            <label className="flex items-center gap-2"><Checkbox checked={tls} onCheckedChange={(v) => setTls(v === true)} /><span className="text-sm">Use TLS</span></label>
          </>
        );
      case "kafka":
        return (
          <>
            <div className="space-y-2">
              <Label htmlFor="brokers">Brokers</Label>
              <Input id="brokers" type="text" placeholder="broker1:9092, broker2:9092" value={brokers} onChange={(e) => setBrokers(e.target.value)} data-testid="check-brokers-input" />
              <p className="text-xs text-muted-foreground">Comma-separated list of broker addresses (host:port)</p>
            </div>
            <div className="space-y-2"><Label htmlFor="topic">Topic (optional)</Label><Input id="topic" type="text" placeholder="my-topic" value={topic} onChange={(e) => setTopic(e.target.value)} data-testid="check-topic-input" /></div>
            <div className="flex gap-4">
              <div className="space-y-2 flex-1"><Label htmlFor="username">SASL Username (optional)</Label><Input id="username" type="text" placeholder="user" value={username} onChange={(e) => setUsername(e.target.value)} data-testid="check-username-input" /></div>
              <div className="space-y-2 flex-1"><Label htmlFor="password">SASL Password (optional)</Label><Input id="password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} data-testid="check-password-input" /></div>
            </div>
            <div className="space-y-3">
              <label className="flex items-center gap-2"><Checkbox checked={tls} onCheckedChange={(v) => setTls(v === true)} /><span className="text-sm">Use TLS</span></label>
              <label className="flex items-center gap-2"><Checkbox checked={produceTest} onCheckedChange={(v) => setProduceTest(v === true)} /><span className="text-sm">Test message production (requires topic)</span></label>
            </div>
          </>
        );
      case "mqtt":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input id="host" type="text" placeholder="broker.example.com" value={host} onChange={(e) => setHost(e.target.value)}
                  className={cn("flex-1", getFieldError(fieldErrors, "host") && "border-destructive")} data-testid="check-host-input" />
                <Input id="port" type="number" placeholder="1883" value={port} onChange={(e) => setPort(e.target.value)}
                  className={cn("w-24", getFieldError(fieldErrors, "port") && "border-destructive")} data-testid="check-port-input" />
              </div>
              {getFieldError(fieldErrors, "host") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "host")}</p>)}
              {getFieldError(fieldErrors, "port") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "port")}</p>)}
            </div>
            <div className="flex gap-4">
              <div className="space-y-2 flex-1"><Label htmlFor="username">Username (optional)</Label><Input id="username" type="text" placeholder="user" value={username} onChange={(e) => setUsername(e.target.value)} data-testid="check-username-input" /></div>
              <div className="space-y-2 flex-1"><Label htmlFor="password">Password (optional)</Label><Input id="password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} data-testid="check-password-input" /></div>
            </div>
            <div className="space-y-2">
              <Label htmlFor="topic">Topic (optional)</Label>
              <Input id="topic" type="text" placeholder="solidping/healthcheck" value={topic} onChange={(e) => setTopic(e.target.value)}
                className={cn(getFieldError(fieldErrors, "topic") && "border-destructive")} data-testid="check-topic-input" />
              {getFieldError(fieldErrors, "topic") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "topic")}</p>)}
            </div>
            <div className="space-y-3">
              <label className="flex items-center gap-2"><Checkbox checked={tls} onCheckedChange={(v) => setTls(v === true)} data-testid="check-tls-checkbox" /><span className="text-sm">Use TLS (port defaults to 8883)</span></label>
            </div>
          </>
        );
      case "a2s":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input id="host" type="text" placeholder="game.example.com" value={host} onChange={(e) => setHost(e.target.value)} className="flex-1" />
                <Input id="port" type="number" placeholder="27015" value={port} onChange={(e) => setPort(e.target.value)} className="w-24" />
              </div>
            </div>
            <div className="flex gap-4">
              <div className="space-y-2 flex-1"><Label htmlFor="minPlayers">Min Players (optional)</Label><Input id="minPlayers" type="number" min={0} placeholder="0" value={minPlayers} onChange={(e) => setMinPlayers(e.target.value)} /><p className="text-xs text-muted-foreground">Alert if fewer players</p></div>
              <div className="space-y-2 flex-1"><Label htmlFor="maxPlayers">Max Players (optional)</Label><Input id="maxPlayers" type="number" min={0} placeholder="0" value={maxPlayersField} onChange={(e) => setMaxPlayersField(e.target.value)} /><p className="text-xs text-muted-foreground">Alert if more players</p></div>
            </div>
          </>
        );
      case "minecraft":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input id="host" type="text" placeholder="play.example.com" value={host} onChange={(e) => setHost(e.target.value)} className="flex-1" />
                <Input id="port" type="number" placeholder={edition === "bedrock" ? "19132" : "25565"} value={port} onChange={(e) => setPort(e.target.value)} className="w-24" />
              </div>
            </div>
            <div className="space-y-2">
              <Label htmlFor="edition">Edition</Label>
              <Select value={edition} onValueChange={setEdition}>
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="java">Java</SelectItem>
                  <SelectItem value="bedrock">Bedrock</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="flex gap-4">
              <div className="space-y-2 flex-1"><Label htmlFor="minPlayers">Min Players (optional)</Label><Input id="minPlayers" type="number" min={0} placeholder="0" value={minPlayers} onChange={(e) => setMinPlayers(e.target.value)} /><p className="text-xs text-muted-foreground">Alert if fewer players</p></div>
              <div className="space-y-2 flex-1"><Label htmlFor="maxPlayers">Max Players (optional)</Label><Input id="maxPlayers" type="number" min={0} placeholder="0" value={maxPlayersField} onChange={(e) => setMaxPlayersField(e.target.value)} /><p className="text-xs text-muted-foreground">Alert if more players</p></div>
            </div>
          </>
        );
      case "snmp":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input id="host" type="text" placeholder="192.168.1.1" value={host} onChange={(e) => setHost(e.target.value)}
                  className={cn("flex-1", getFieldError(fieldErrors, "host") && "border-destructive")} data-testid="check-host-input" />
                <Input id="port" type="number" placeholder="161" value={port} onChange={(e) => setPort(e.target.value)}
                  className={cn("w-24", getFieldError(fieldErrors, "port") && "border-destructive")} data-testid="check-port-input" />
              </div>
              {getFieldError(fieldErrors, "host") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "host")}</p>)}
              {getFieldError(fieldErrors, "port") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "port")}</p>)}
            </div>
            <div className="space-y-2">
              <Label htmlFor="oid">OID</Label>
              <Input id="oid" type="text" placeholder=".1.3.6.1.2.1.1.1.0" value={oid} onChange={(e) => setOid(e.target.value)}
                className={cn(getFieldError(fieldErrors, "oid") && "border-destructive")} data-testid="check-oid-input" />
              {getFieldError(fieldErrors, "oid") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "oid")}</p>)}
            </div>
            <div className="space-y-2"><Label htmlFor="community">Community (optional, default: public)</Label><Input id="community" type="text" placeholder="public" value={community} onChange={(e) => setCommunity(e.target.value)} data-testid="check-community-input" /></div>
            <div className="flex gap-4">
              <div className="space-y-2 flex-1"><Label htmlFor="expectedValue">Expected Value (optional)</Label><Input id="expectedValue" type="text" placeholder="" value={expectedValue} onChange={(e) => setExpectedValue(e.target.value)} data-testid="check-expected-value-input" /></div>
              <div className="space-y-2 w-40">
                <Label htmlFor="snmpOperator">Operator</Label>
                <Select value={snmpOperator} onValueChange={setSnmpOperator}>
                  <SelectTrigger data-testid="check-operator-select"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="equals">Equals</SelectItem><SelectItem value="not_equals">Not Equals</SelectItem><SelectItem value="contains">Contains</SelectItem><SelectItem value="greater_than">Greater Than</SelectItem><SelectItem value="less_than">Less Than</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
          </>
        );
      case "docker":
        return (
          <>
            <div className="space-y-2">
              <Label htmlFor="containerName">Container Name</Label>
              <Input id="containerName" type="text" placeholder="postgres" value={containerName} onChange={(e) => setContainerName(e.target.value)}
                className={cn(getFieldError(fieldErrors, "containerName") && "border-destructive")} data-testid="check-container-name-input" />
              {getFieldError(fieldErrors, "containerName") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "containerName")}</p>)}
            </div>
            <div className="space-y-2">
              <Label htmlFor="containerId">Container ID (optional, alternative to name)</Label>
              <Input id="containerId" type="text" placeholder="abc123def456" value={containerId} onChange={(e) => setContainerId(e.target.value)} data-testid="check-container-id-input" />
            </div>
            <div className="space-y-2">
              <Label htmlFor="host">Docker Host (optional)</Label>
              <Input id="host" type="text" placeholder="unix:///var/run/docker.sock" value={host} onChange={(e) => setHost(e.target.value)} data-testid="check-host-input" />
              <p className="text-xs text-muted-foreground">Default: unix:///var/run/docker.sock. Use tcp://host:port for remote Docker daemons.</p>
            </div>
            <div className="space-y-2">
              <button type="button" className="text-sm underline" onClick={() => setDockerRestartLoopOpen((v) => !v)}>
                {dockerRestartLoopOpen ? "▼ " : "▶ "}Restart-loop detection (advanced)
              </button>
              {dockerRestartLoopOpen && (
                <div className="space-y-2 pl-4 border-l">
                  <p className="text-xs text-muted-foreground">
                    Flag a running container as crash-looping when it has restarted at least N
                    times and (re)started within the recency window. Leave Min Restarts empty
                    (or 0) to disable. A detected loop reports a Warning (amber) — it counts as
                    up and does not page.
                  </p>
                  <div className="space-y-1">
                    <Label htmlFor="restartLoopMinRestarts">Min Restarts (0 = disabled)</Label>
                    <Input id="restartLoopMinRestarts" type="number" min="0" placeholder="3" value={restartLoopMinRestarts}
                      onChange={(e) => setRestartLoopMinRestarts(e.target.value)} data-testid="check-restart-loop-min-input" />
                  </div>
                  <div className="space-y-1">
                    <Label htmlFor="restartLoopWindowSeconds">Window (seconds, default 120)</Label>
                    <Input id="restartLoopWindowSeconds" type="number" min="1" placeholder="120" value={restartLoopWindowSeconds}
                      onChange={(e) => setRestartLoopWindowSeconds(e.target.value)} data-testid="check-restart-loop-window-input" />
                  </div>
                </div>
              )}
            </div>
          </>
        );
      case "browser":
        return (
          <>
            <div className="space-y-2">
              <Label htmlFor="url">URL</Label>
              <Input id="url" type="url" placeholder="https://example.com" value={url} onChange={(e) => setUrl(e.target.value)}
                className={cn(getFieldError(fieldErrors, "url") && "border-destructive")} data-testid="check-url-input" />
              {getFieldError(fieldErrors, "url") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "url")}</p>)}
            </div>
            <div className="space-y-2">
              <Label htmlFor="waitSelector">Wait Selector (optional)</Label>
              <Input id="waitSelector" type="text" placeholder="#main-content" value={waitSelector} onChange={(e) => setWaitSelector(e.target.value)} data-testid="check-wait-selector-input" />
              <p className="text-xs text-muted-foreground">CSS selector to wait for before checking. Leave empty to wait for body.</p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="keyword">Keyword (optional)</Label>
              <Input id="keyword" type="text" placeholder="Welcome" value={keyword} onChange={(e) => setKeyword(e.target.value)} data-testid="check-keyword-input" />
              <p className="text-xs text-muted-foreground">Text to search for in the rendered page content.</p>
            </div>
          </>
        );
      case "freebox_line": {
        // Filter by the canSource capability rather than a hard-coded type so
        // future data-source integrations are picked up automatically. Today
        // this resolves to freebox only.
        const freeboxConnections = (connections ?? []).filter((c) =>
          canSource(c.type),
        );
        return (
          <>
            <div className="space-y-2">
              <Label htmlFor="freeboxConnectionUid">{t("freeboxLine.connectionUid")}</Label>
              {freeboxConnections.length === 0 ? (
                <Alert>
                  <AlertDescription>
                    {t("freeboxLine.noConnections")}{" "}
                    <Link to="/orgs/$org/integrations" params={{ org }} className="underline">Integrations</Link>
                  </AlertDescription>
                </Alert>
              ) : (
                <Select value={freeboxConnectionUid} onValueChange={setFreeboxConnectionUid}>
                  <SelectTrigger id="freeboxConnectionUid" data-testid="check-freebox-connection-select">
                    <SelectValue placeholder={t("freeboxLine.connectionUid")} />
                  </SelectTrigger>
                  <SelectContent>
                    {freeboxConnections.map((c) => (
                      <SelectItem key={c.uid} value={c.uid}>{c.name}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
              <p className="text-xs text-muted-foreground">{t("freeboxLine.connectionUidHelp")}</p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="freeboxLinkType">{t("freeboxLine.linkType")}</Label>
              <Select value={freeboxLinkType} onValueChange={setFreeboxLinkType}>
                <SelectTrigger id="freeboxLinkType" data-testid="check-freebox-linktype-select">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="xdsl">{t("freeboxLine.linkTypeXdsl")}</SelectItem>
                  <SelectItem value="ftth">{t("freeboxLine.linkTypeFtth")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <button type="button" className="text-sm underline" onClick={() => setFreeboxThresholdsOpen((v) => !v)}>
                {freeboxThresholdsOpen ? "▼ " : "▶ "}{t("freeboxLine.advancedThresholds")}
              </button>
              {freeboxThresholdsOpen && (
                <div className="space-y-2 pl-4 border-l">
                  <p className="text-xs text-muted-foreground">{t("freeboxLine.thresholdsHelp")}</p>
                  {freeboxLinkType === "xdsl" && (
                    <>
                      <div className="space-y-1">
                        <Label htmlFor="freeboxMinSyncRate">{t("freeboxLine.minSyncRateDownKbps")}</Label>
                        <Input id="freeboxMinSyncRate" type="number" min="0" placeholder="0" value={freeboxMinSyncRate}
                          onChange={(e) => setFreeboxMinSyncRate(e.target.value)} data-testid="check-freebox-minsync-input" />
                      </div>
                      <div className="space-y-1">
                        <Label htmlFor="freeboxMinSnr">{t("freeboxLine.minSnrMarginDownDb")}</Label>
                        <Input id="freeboxMinSnr" type="number" min="0" placeholder="0" value={freeboxMinSnrDb}
                          onChange={(e) => setFreeboxMinSnrDb(e.target.value)} data-testid="check-freebox-minsnr-input" />
                      </div>
                      <div className="space-y-1">
                        <Label htmlFor="freeboxMaxAttn">{t("freeboxLine.maxAttenuationDb")}</Label>
                        <Input id="freeboxMaxAttn" type="number" min="0" placeholder="0" value={freeboxMaxAttnDb}
                          onChange={(e) => setFreeboxMaxAttnDb(e.target.value)} data-testid="check-freebox-maxattn-input" />
                      </div>
                      <div className="space-y-1">
                        <Label htmlFor="freeboxMaxCrc">{t("freeboxLine.maxCrcErrorsPerRun")}</Label>
                        <Input id="freeboxMaxCrc" type="number" min="0" placeholder="0" value={freeboxMaxCrcErrors}
                          onChange={(e) => setFreeboxMaxCrcErrors(e.target.value)} data-testid="check-freebox-maxcrc-input" />
                      </div>
                    </>
                  )}
                  {freeboxLinkType === "ftth" && (
                    <>
                      <div className="space-y-1">
                        <Label htmlFor="freeboxMinRxMw">{t("freeboxLine.minRxPowerMw")}</Label>
                        <Input id="freeboxMinRxMw" type="number" min="0" step="0.001" placeholder="0" value={freeboxMinRxMw}
                          onChange={(e) => setFreeboxMinRxMw(e.target.value)} data-testid="check-freebox-minrx-input" />
                      </div>
                      <div className="space-y-1">
                        <Label htmlFor="freeboxMaxRxMw">{t("freeboxLine.maxRxPowerMw")}</Label>
                        <Input id="freeboxMaxRxMw" type="number" min="0" step="0.001" placeholder="0" value={freeboxMaxRxMw}
                          onChange={(e) => setFreeboxMaxRxMw(e.target.value)} data-testid="check-freebox-maxrx-input" />
                      </div>
                    </>
                  )}
                </div>
              )}
            </div>
          </>
        );
      }
      case "dnsbl":
        return (
          <>
            <div className="space-y-2">
              <Label htmlFor="dnsblTarget">{t("dnsbl.target")}</Label>
              <Input id="dnsblTarget" type="text" placeholder="203.0.113.10" value={dnsblTarget}
                onChange={(e) => setDnsblTarget(e.target.value)}
                className={cn(getFieldError(fieldErrors, "target") && "border-destructive")}
                data-testid="check-dnsbl-target-input" />
              <p className="text-xs text-muted-foreground">{t("dnsbl.targetHelp")}</p>
              {getFieldError(fieldErrors, "target") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "target")}</p>)}
            </div>
            <div className="space-y-2">
              <Label htmlFor="dnsblBlocklists">{t("dnsbl.blocklists")}</Label>
              <Textarea id="dnsblBlocklists" rows={4}
                placeholder={"zen.spamhaus.org\nbl.spamcop.net"} value={dnsblBlocklists}
                onChange={(e) => setDnsblBlocklists(e.target.value)}
                data-testid="check-dnsbl-blocklists-input" />
              <p className="text-xs text-muted-foreground">{t("dnsbl.blocklistsHelp")}</p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="dnsblNameserver">{t("dnsbl.nameserver")}</Label>
              <Input id="dnsblNameserver" type="text" placeholder="127.0.0.1:53" value={dnsblNameserver}
                onChange={(e) => setDnsblNameserver(e.target.value)}
                className={cn(getFieldError(fieldErrors, "nameserver") && "border-destructive")}
                data-testid="check-dnsbl-nameserver-input" />
              <p className="text-xs text-muted-foreground">{t("dnsbl.nameserverHelp")}</p>
              {getFieldError(fieldErrors, "nameserver") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "nameserver")}</p>)}
            </div>
          </>
        );
      case "sip":
        return (
          <>
            <div className="space-y-2">
              <Label>{t("form.host")}</Label>
              <div className="flex gap-2">
                <Input id="host" type="text" placeholder="pbx.example.com" value={host}
                  onChange={(e) => setHost(e.target.value)} className="flex-1"
                  data-testid="check-sip-host-input" />
                <Input id="port" type="number"
                  placeholder={sipTransport === "tls" ? "5061" : "5060"} value={port}
                  onChange={(e) => setPort(e.target.value)} className="w-24"
                  data-testid="check-sip-port-input" />
              </div>
            </div>
            <div className="flex gap-4">
              <div className="space-y-2 flex-1">
                <Label htmlFor="sipTransport">{t("sip.transport")}</Label>
                <Select value={sipTransport} onValueChange={setSipTransport}>
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
                <Select value={sipMode} onValueChange={setSipMode}>
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
              <Input id="domain" type="text" placeholder="defaults to host" value={domain}
                onChange={(e) => setDomain(e.target.value)} data-testid="check-sip-domain-input" />
              <p className="text-xs text-muted-foreground">{t("sip.domainHelp")}</p>
            </div>
            {sipMode === "register" ? (
              <div className="flex gap-4">
                <div className="space-y-2 flex-1">
                  <Label htmlFor="username">{t("sip.username")}</Label>
                  <Input id="username" type="text" placeholder="1001" value={username}
                    onChange={(e) => setUsername(e.target.value)} data-testid="check-sip-username-input" />
                </div>
                <div className="space-y-2 flex-1">
                  <Label htmlFor="password">{t("sip.password")}</Label>
                  <Input id="password" type="password" value={password}
                    onChange={(e) => setPassword(e.target.value)} data-testid="check-sip-password-input" />
                  {initialData?.configPrivateKeys?.includes("password") && !password && (
                    <p className="text-xs text-muted-foreground">
                      <span className="font-mono tracking-widest">••••</span>
                      {" "}
                      <span className="italic">{t("sip.passwordEncrypted")}</span>
                    </p>
                  )}
                </div>
              </div>
            ) : (
              <div className="space-y-2">
                <Label htmlFor="sipExpectStatus">{t("sip.expectStatus")}</Label>
                <Input id="sipExpectStatus" type="text" placeholder="200,405" value={sipExpectStatus}
                  onChange={(e) => setSipExpectStatus(e.target.value)} data-testid="check-sip-expect-status-input" />
                <p className="text-xs text-muted-foreground">{t("sip.expectStatusHelp")}</p>
              </div>
            )}
          </>
        );
      case "sleep":
        return (
          <>
            <Alert>
              <AlertDescription className="text-xs">
                Synthetic checker — sleeps for the configured duration and performs no network I/O. Useful for testing scheduler/load behavior, not a real availability probe.
              </AlertDescription>
            </Alert>
            <div className="flex gap-4">
              <div className="space-y-2 w-40">
                <Label htmlFor="sleepMs">Sleep duration (ms)</Label>
                <Input id="sleepMs" type="number" min={1} placeholder="500" value={sleepMs} onChange={(e) => setSleepMs(e.target.value)}
                  className={cn(getFieldError(fieldErrors, "sleep_ms") && "border-destructive")} data-testid="check-sleep-ms-input" />
                {getFieldError(fieldErrors, "sleep_ms") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "sleep_ms")}</p>)}
              </div>
              <div className="space-y-2 w-40">
                <Label htmlFor="jitterMs">Jitter (ms, optional)</Label>
                <Input id="jitterMs" type="number" min={0} placeholder="0" value={jitterMs} onChange={(e) => setJitterMs(e.target.value)}
                  className={cn(getFieldError(fieldErrors, "jitter_ms") && "border-destructive")} data-testid="check-jitter-ms-input" />
                {getFieldError(fieldErrors, "jitter_ms") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "jitter_ms")}</p>)}
              </div>
            </div>
            <p className="text-xs text-muted-foreground">± random variation applied to the sleep duration. Must be less than the sleep duration itself.</p>
            <div className="space-y-2 w-40">
              <Label htmlFor="sleepStatus">Forced status (optional)</Label>
              <Select value={sleepStatus} onValueChange={setSleepStatus}>
                <SelectTrigger id="sleepStatus" data-testid="check-sleep-status-select"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="up">Up (default)</SelectItem>
                  <SelectItem value="down">Down</SelectItem>
                  <SelectItem value="timeout">Timeout</SelectItem>
                  <SelectItem value="error">Error</SelectItem>
                </SelectContent>
              </Select>
              {getFieldError(fieldErrors, "status") && (<p className="text-xs text-destructive">{getFieldError(fieldErrors, "status")}</p>)}
            </div>
          </>
        );
      case "heartbeat":
        return (
          <p className="text-sm text-muted-foreground">No additional configuration needed. A heartbeat URL will be generated after creation.</p>
        );
      case "email":
        if (!emailDomain) {
          return (
            <Alert variant="destructive">
              <AlertDescription>
                Email inbox not configured. Ask your administrator to set it up under Server &rarr; Email Inbox.
              </AlertDescription>
            </Alert>
          );
        }
        return (
          <p className="text-sm text-muted-foreground">
            An email address will be generated for this check. Send any email to that address to report a successful run. Use plus-addressing (<code className="font-mono">token+down@…</code>) or <code className="font-mono">[DOWN]</code> in the subject to report failure.
          </p>
        );
    }
  };

  const isEdit = mode === "edit";
  const title = isEdit ? "Edit Check" : "New Check";
  const subtitle = isEdit ? "Update the monitoring check parameters" : "Create a new monitoring check";
  const submitLabel = isEdit ? "Save Changes" : "Create Check";
  const pendingLabel = isEdit ? "Saving..." : "Creating...";

  const selectedTypeLabel = checkTypes.find((t) => t.value === type)?.label || type;

  // ── Progressive-disclosure section summaries + open-on-content ──
  const labelCount = Object.keys(labels).length;
  const groupName = checkGroupUid
    ? checkGroups?.find((g) => g.uid === checkGroupUid)?.name
    : undefined;
  const orgCustomized = labelCount > 0 || !!checkGroupUid;
  const orgSummaryParts: string[] = [];
  if (slug) orgSummaryParts.push(`slug ${slug}`);
  if (labelCount > 0)
    orgSummaryParts.push(`${labelCount} label${labelCount === 1 ? "" : "s"}`);
  if (groupName) orgSummaryParts.push(`group ${groupName}`);
  const orgSummary = orgSummaryParts.join(" · ") || "slug auto-generated";

  const depCount = dependsOnParents?.length ?? 0;
  const depsSummary =
    depCount > 0 ? `${depCount} parent${depCount === 1 ? "" : "s"}` : "None";

  const incidentCustomized =
    confirmationPeriodSeconds.trim() !== "" ||
    recoveryPeriodSeconds.trim() !== "";
  const incidentSummary = `confirm ${
    confirmationPeriodSeconds.trim() || "120"
  }s, recover ${recoveryPeriodSeconds.trim() || "120"}s${
    incidentCustomized ? "" : " (defaults)"
  }`;

  const flappingCustomized = [
    reopenCooldownMultiplier,
    flappingWindowSeconds,
    flapBackoffFactor,
    maxRecoveryMultiplier,
  ].some((v) => v.trim() !== "");
  const flappingWindowLabel =
    flappingWindowSeconds.trim() !== "" &&
    (parseInt(flappingWindowSeconds, 10) || 0) > 0
      ? formatDuration(parseInt(flappingWindowSeconds, 10))
      : "6h";
  const flappingSummary = `window ${flappingWindowLabel}, cooldown ×${
    reopenCooldownMultiplier.trim() || "5"
  }${flappingCustomized ? "" : " (defaults)"}`;

  const timeoutError = getFieldError(fieldErrors, "timeout");
  const advancedCustomized = timeoutSeconds.trim() !== "";
  const advancedSummary = advancedCustomized
    ? `timeout ${timeoutSeconds}s`
    : "timeout 15s (default)";
  const showGroup = (checkGroups?.length ?? 0) > 0;

  // A section opens on load when it holds non-default values OR is the target of
  // a `?section=<id>` deep-link (which the mount effect above also scrolls to).
  const sectionOpen = (id: string, hasValues: boolean) =>
    hasValues || initialSection === id;

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

      <form onSubmit={handleSubmit} className="space-y-4">
        {error && (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}

        {/* 1. Identity & target — always visible */}
        <Card>
          <CardHeader className="flex flex-row items-start justify-between gap-4 space-y-0">
            <div className="space-y-1.5">
              <CardTitle>Identity &amp; target</CardTitle>
              <CardDescription>What to monitor, and how to find it later</CardDescription>
            </div>
            <label
              htmlFor="check-enabled"
              className="flex shrink-0 items-center gap-2 pt-1 text-sm font-medium"
            >
              <span className="text-muted-foreground">Enabled</span>
              <Switch
                id="check-enabled"
                checked={enabled}
                onCheckedChange={setEnabled}
                data-testid="check-enabled-switch"
              />
            </label>
          </CardHeader>
          <CardContent className="space-y-4">
            {/* Check Type - searchable combobox + template button */}
            <div className="space-y-2">
              <Label htmlFor="type">Type</Label>
              {isEdit ? (
                <Input id="type" value={selectedTypeLabel} disabled data-testid="check-type-select" />
              ) : (
                <div className="flex gap-2">
                  <Popover open={typeSearchOpen} onOpenChange={setTypeSearchOpen}>
                    <PopoverTrigger asChild>
                      <Button variant="outline" role="combobox" aria-expanded={typeSearchOpen}
                        className="flex-1 justify-between font-normal" data-testid="check-type-select">
                        <span>{selectedTypeLabel}</span>
                        <ChevronsUpDown className="ml-2 h-4 w-4 shrink-0 opacity-50" />
                      </Button>
                    </PopoverTrigger>
                    <PopoverContent className="p-0 max-h-72">
                      <div className="flex items-center border-b px-3 py-2">
                        <Search className="mr-2 h-4 w-4 shrink-0 opacity-50" />
                        <input
                          ref={searchInputRef}
                          placeholder="Search check types..."
                          value={typeSearch}
                          onChange={(e) => setTypeSearch(e.target.value)}
                          className="flex h-8 w-full bg-transparent text-sm outline-none placeholder:text-muted-foreground"
                        />
                      </div>
                      <div className="max-h-56 overflow-y-auto p-1">
                        {filteredCheckTypes.length === 0 ? (
                          <div className="px-3 py-2 text-sm text-muted-foreground">No check types found</div>
                        ) : (
                          filteredCheckTypes.map((ct) => (
                            <button
                              key={ct.value}
                              type="button"
                              role="option"
                              aria-selected={type === ct.value}
                              className={cn(
                                "flex w-full items-start gap-2 rounded-sm px-2 py-1.5 text-left text-sm hover:bg-accent cursor-pointer",
                                type === ct.value && "bg-accent"
                              )}
                              onClick={() => {
                                const newType = ct.value;
                                setType(newType);
                                setPeriod(getDefaultPeriodHMS(newType));
                                onTypeChange?.(newType);
                                setTypeSearchOpen(false);
                                setTypeSearch("");
                              }}
                            >
                              <Check className={cn("mt-0.5 h-4 w-4 shrink-0", type === ct.value ? "opacity-100" : "opacity-0")} />
                              <div>
                                <div className="font-medium flex items-center gap-1.5">
                                  {ct.label}
                                  {ct.synthetic && (
                                    <Badge variant="secondary" className="text-[10px] px-1 py-0 font-normal">synthetic</Badge>
                                  )}
                                </div>
                                <div className="text-xs text-muted-foreground">{ct.description}</div>
                              </div>
                            </button>
                          ))
                        )}
                      </div>
                    </PopoverContent>
                  </Popover>
                  <Popover
                    open={samplePickerOpen}
                    onOpenChange={(open) => {
                      setSamplePickerOpen(open);
                      if (open) {
                        void fetchSamples();
                      }
                    }}
                  >
                    <PopoverTrigger asChild>
                      <Button
                        type="button"
                        variant="secondary"
                        data-testid="check-load-template-button"
                        disabled={!type}
                      >
                        {t("loadSample", "Load sample…")}
                      </Button>
                    </PopoverTrigger>
                    <PopoverContent className="w-[320px] p-1" align="end">
                      {isFetchingSamples ? (
                        <div className="flex items-center justify-center py-4">
                          <Loader2 className="h-4 w-4 animate-spin" />
                        </div>
                      ) : !fetchedSamples || fetchedSamples.length === 0 ? (
                        <div className="px-3 py-2 text-sm text-muted-foreground">
                          {t("noSamples", "No samples for this type")}
                        </div>
                      ) : (
                        <div className="grid max-h-[280px] gap-0.5 overflow-y-auto">
                          {fetchedSamples.map((sample) => (
                            <button
                              key={sample.slug}
                              type="button"
                              className="rounded-md px-3 py-2 text-left text-sm transition-colors hover:bg-accent"
                              data-testid={`check-sample-${sample.slug}`}
                              onClick={() => {
                                applySample(sample);
                                setSamplePickerOpen(false);
                              }}
                            >
                              {sample.name}
                            </button>
                          ))}
                        </div>
                      )}
                    </PopoverContent>
                  </Popover>
                </div>
              )}
            </div>

            {/* Protocol-specific config (URL/host/port/…) */}
            {renderConfigFields()}

            <div className="space-y-2">
              <Label htmlFor="name">Name {mode === "create" && "(optional)"}</Label>
              <Input id="name" type="text" placeholder="My Check" value={name} onChange={(e) => setName(e.target.value)} data-testid="check-name-input" />
              {mode === "create" && (<p className="text-xs text-muted-foreground">If not provided, a name will be auto-generated</p>)}
            </div>
          </CardContent>
        </Card>

        {/* 2. Scheduling — always visible */}
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Scheduling</CardTitle>
            <CardDescription>How often the check runs, and from where</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="period">
                {isPassiveType(type) ? "Expected Interval" : "Check Interval"}
              </Label>
              {isPassiveType(type) ? (
                <div className="flex gap-2">
                  <Input id="period" type="number" min={1} value={periodValue}
                    onChange={(e) => setPeriodValue(parseInt(e.target.value, 10) || 1)}
                    className="w-24" data-testid="check-period-input" />
                  <Select value={periodUnit} onValueChange={(v) => setPeriodUnit(v as PeriodUnit)}>
                    <SelectTrigger data-testid="check-period-unit-select" className="flex-1"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      {periodUnits.map((u) => (<SelectItem key={u.value} value={u.value}>{u.label}</SelectItem>))}
                    </SelectContent>
                  </Select>
                </div>
              ) : (
                <Select value={period} onValueChange={setPeriod}>
                  <SelectTrigger data-testid="check-period-select"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    {intervalOptions.map((opt) => (
                      <SelectItem key={opt.value} value={opt.value}>{opt.label}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
              {type === "heartbeat" && (
                <p className="text-xs text-muted-foreground">Check will be marked as down if no heartbeat is received within this interval</p>
              )}
              {type === "email" && (
                <p className="text-xs text-muted-foreground">Check will be marked as down if no email is received within this interval</p>
              )}
            </div>

            {showRegions && (
              <div className="space-y-2">
                <Label>Regions</Label>
                <div className="grid grid-cols-2 gap-2">
                  {availableRegions?.map((region) => (
                    <label key={region.slug} className="flex items-center gap-2 rounded-md border p-2 cursor-pointer hover:bg-muted/50">
                      <Checkbox checked={selectedRegions.includes(region.slug)} onCheckedChange={() => toggleRegion(region.slug)} />
                      <span className="text-sm">{region.emoji} {region.name}</span>
                    </label>
                  ))}
                </div>
                <p className="text-xs text-muted-foreground">Select the regions where this check should run</p>
              </div>
            )}
          </CardContent>
        </Card>

        {/* 3. Notifications — always visible */}
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Notifications</CardTitle>
            <CardDescription>Who gets paged when this check fails</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <NotifyViaSection
              org={org}
              connections={connections}
              selected={connectionUids ?? []}
              onToggle={toggleConnection}
            />
          </CardContent>
        </Card>

        {/* 4. Collapsible sections — collapsed unless customized */}
        <CollapsibleSection
          id="organization"
          data-testid="section-organization-trigger"
          title="Organization"
          summary={orgSummary}
          customized={orgCustomized}
          defaultOpen={sectionOpen("organization", orgCustomized)}
          expandSignal={slugError ? submitAttempts : 0}
        >

            <div className="space-y-2">
              <Label htmlFor="slug">Slug {mode === "create" && "(optional)"}</Label>
              <Input id="slug" type="text" placeholder="my-check" value={slug} onChange={(e) => setSlug(e.target.value)} data-testid="check-slug-input" className={slugError ? "border-destructive" : ""} />
              {slugError ? (
                <p className="text-xs text-destructive">{slugError}</p>
              ) : (
                <p className="text-xs text-muted-foreground">URL-friendly identifier for the check</p>
              )}
            </div>

            <div className="space-y-2">
              <Label>Labels</Label>
              <LabelInput
                org={org}
                value={labels}
                onChange={(next) => {
                  setLabels(next);
                  setLabelsDirty(true);
                }}
              />
              <p className="text-xs text-muted-foreground">Optional key/value tags for grouping and filtering.</p>
            </div>

            {showGroup && (
              <div className="space-y-2">
                <Label htmlFor="group">Group (optional)</Label>
                <Select value={checkGroupUid || "none"} onValueChange={(v) => setCheckGroupUid(v === "none" ? "" : v)}>
                  <SelectTrigger data-testid="check-group-select"><SelectValue placeholder="No group" /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">No group</SelectItem>
                    {checkGroups?.map((g) => (<SelectItem key={g.uid} value={g.uid}>{g.name}</SelectItem>))}
                  </SelectContent>
                </Select>
              </div>
            )}
        </CollapsibleSection>

        <CollapsibleSection
          id="dependencies"
          data-testid="section-dependencies-trigger"
          title="Dependencies"
          summary={depsSummary}
          customized={depCount > 0}
          defaultOpen={sectionOpen("dependencies", depCount > 0)}
        >
          <DependsOnFormSection
            org={org}
            checkUid={mode === "edit" ? initialData?.uid : undefined}
            parents={dependsOnParents ?? []}
            onAdd={addParent}
            onRemove={removeParent}
          />
        </CollapsibleSection>

        <CollapsibleSection
          id="incident-tracking"
          data-testid="section-incident-tracking-trigger"
          title="Incident tracking"
          summary={incidentSummary}
          customized={incidentCustomized}
          defaultOpen={sectionOpen("incident-tracking", incidentCustomized)}
        >
          <div className="grid grid-cols-2 gap-4">
            <div className="space-y-1">
              <Label htmlFor="confirmationPeriodSeconds" className="text-sm">{t("form.confirmationPeriod")}</Label>
              <Input id="confirmationPeriodSeconds" data-testid="confirmation-period-input" type="number" min={0} max={86400} placeholder="120 (default)" value={confirmationPeriodSeconds} onChange={(e) => setConfirmationPeriodSeconds(e.target.value)} />
              <p className="text-xs text-muted-foreground">{t("form.confirmationPeriodHelp")}</p>
              {confirmationPeriodSeconds.trim() !== "" && (
                <p className="text-xs text-muted-foreground break-words" data-testid="confirmation-period-estimate">
                  {describePeriod(
                    parseInt(confirmationPeriodSeconds, 10) || 0,
                    estimateIntervalSeconds,
                    "confirmation",
                    t,
                  )}
                </p>
              )}
            </div>
            <div className="space-y-1">
              <Label htmlFor="recoveryPeriodSeconds" className="text-sm">{t("form.recoveryPeriod")}</Label>
              <Input id="recoveryPeriodSeconds" data-testid="recovery-period-input" type="number" min={0} max={86400} placeholder="120 (default)" value={recoveryPeriodSeconds} onChange={(e) => setRecoveryPeriodSeconds(e.target.value)} />
              <p className="text-xs text-muted-foreground">{t("form.recoveryPeriodHelp")}</p>
              {recoveryPeriodSeconds.trim() !== "" && (
                <p className="text-xs text-muted-foreground break-words" data-testid="recovery-period-estimate">
                  {describePeriod(
                    parseInt(recoveryPeriodSeconds, 10) || 0,
                    estimateIntervalSeconds,
                    "recovery",
                    t,
                  )}
                </p>
              )}
            </div>
          </div>
        </CollapsibleSection>

        <CollapsibleSection
          id="flapping"
          data-testid="section-flapping-trigger"
          title={t("form.flapping")}
          summary={flappingSummary}
          customized={flappingCustomized}
          defaultOpen={sectionOpen("flapping", flappingCustomized)}
        >
          <p className="text-xs text-muted-foreground">{t("form.flappingHelp")}</p>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div className="space-y-1">
              <Label htmlFor="reopenCooldownMultiplier" className="text-sm">{t("form.reopenCooldown")}</Label>
              <Input id="reopenCooldownMultiplier" data-testid="reopen-cooldown-input" type="number" min={0} placeholder="5 (default)" value={reopenCooldownMultiplier} onChange={(e) => setReopenCooldownMultiplier(e.target.value)} />
              <p className="text-xs text-muted-foreground">{t("form.reopenCooldownHelp")}</p>
            </div>
            <div className="space-y-1">
              <Label htmlFor="flappingWindowSeconds" className="text-sm">{t("form.flappingWindow")}</Label>
              <Input id="flappingWindowSeconds" data-testid="flapping-window-input" type="number" min={0} placeholder="21600 (default)" value={flappingWindowSeconds} onChange={(e) => setFlappingWindowSeconds(e.target.value)} />
              <p className="text-xs text-muted-foreground">{t("form.flappingWindowHelp")}</p>
              {flappingWindowSeconds.trim() !== "" && (parseInt(flappingWindowSeconds, 10) || 0) > 0 && (
                <p className="text-xs text-muted-foreground break-words" data-testid="flapping-window-estimate">
                  {t("form.periodEstimate", { duration: formatDuration(parseInt(flappingWindowSeconds, 10) || 0) })}
                </p>
              )}
            </div>
            <div className="space-y-1">
              <Label htmlFor="flapBackoffFactor" className="text-sm">{t("form.flapBackoffFactor")}</Label>
              <Input id="flapBackoffFactor" data-testid="flap-backoff-input" type="number" min={1} placeholder="2 (default)" value={flapBackoffFactor} onChange={(e) => setFlapBackoffFactor(e.target.value)} />
              <p className="text-xs text-muted-foreground">{t("form.flapBackoffFactorHelp")}</p>
            </div>
            <div className="space-y-1">
              <Label htmlFor="maxRecoveryMultiplier" className="text-sm">{t("form.maxRecoveryMultiplier")}</Label>
              <Input id="maxRecoveryMultiplier" data-testid="max-recovery-multiplier-input" type="number" min={1} placeholder="8 (default)" value={maxRecoveryMultiplier} onChange={(e) => setMaxRecoveryMultiplier(e.target.value)} />
              <p className="text-xs text-muted-foreground">{t("form.maxRecoveryMultiplierHelp")}</p>
            </div>
          </div>
        </CollapsibleSection>

        {!isPassiveType(type) && (
          <CollapsibleSection
            id="advanced"
            data-testid="section-advanced-trigger"
            title="Advanced"
            summary={advancedSummary}
            customized={advancedCustomized}
            defaultOpen={sectionOpen("advanced", advancedCustomized || !!timeoutError)}
            expandSignal={timeoutError ? submitAttempts : 0}
          >
            <div className="space-y-2">
              <Label htmlFor="check-timeout">Timeout (optional)</Label>
              <Input
                id="check-timeout"
                type="number"
                min={1}
                max={30}
                step={1}
                placeholder="15 seconds (default)"
                value={timeoutSeconds}
                onChange={(e) => setTimeoutSeconds(e.target.value)}
                className={cn(getFieldError(fieldErrors, "timeout") && "border-destructive")}
                data-testid="check-timeout-input"
              />
              {getFieldError(fieldErrors, "timeout") ? (
                <p className="text-xs text-destructive">{getFieldError(fieldErrors, "timeout")}</p>
              ) : (
                <p className="text-xs text-muted-foreground">
                  Seconds a single probe may run (1–30). Empty uses the default of 15 seconds.
                </p>
              )}
            </div>
          </CollapsibleSection>
        )}

        {/* Sticky footer — save without scrolling past the tuning knobs */}
        <div className="sticky bottom-0 z-10 flex justify-end gap-2 rounded-lg border bg-background/95 px-4 py-3 shadow-sm backdrop-blur supports-[backdrop-filter]:bg-background/80">
          <Button type="button" variant="outline" onClick={onCancel}>Cancel</Button>
          <Button type="submit" disabled={isPending} data-testid="check-submit-button">
            {isPending ? (<><Loader2 className="mr-2 h-4 w-4 animate-spin" />{pendingLabel}</>) : submitLabel}
          </Button>
        </div>
      </form>
    </div>
  );
}

interface DependsOnFormSectionProps {
  org: string;
  checkUid: string | undefined;
  parents: { uid: string; label: string }[];
  onAdd: (uid: string, label: string) => void;
  onRemove: (uid: string) => void;
}

function DependsOnFormSection({
  org,
  checkUid,
  parents,
  onAdd,
  onRemove,
}: DependsOnFormSectionProps) {
  const excludeUids = useMemo(() => {
    const set = new Set<string>(parents.map((p) => p.uid));
    if (checkUid) set.add(checkUid);
    return set;
  }, [parents, checkUid]);

  return (
    <div className="space-y-2">
      <Label>Dependencies</Label>
      <p className="text-xs text-muted-foreground">
        Parents whose downtime should suppress incident alerts on this check.
        Edit kind/description on the check detail page after save.
      </p>
      <div className="space-y-2">
        {parents.map((p) => (
          <div
            key={p.uid}
            className="flex items-center gap-2 rounded-md border p-2"
          >
            <span className="flex-1 truncate text-sm">{p.label}</span>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              onClick={() => onRemove(p.uid)}
              aria-label="Remove parent"
            >
              <X className="h-3.5 w-3.5" />
            </Button>
          </div>
        ))}
        <CheckPicker
          org={org}
          excludeUids={excludeUids}
          onChange={(uid, c) => {
            if (uid) onAdd(uid, c?.name || c?.slug || uid);
          }}
          placeholder="Add a parent check…"
        />
      </div>
    </div>
  );
}

interface NotifyViaSectionProps {
  org: string;
  connections: ReturnType<typeof useIntegrations>["data"];
  selected: string[];
  onToggle: (uid: string) => void;
}

function NotifyViaSection({ org, connections, selected, onToggle }: NotifyViaSectionProps) {
  // Only notify-capable integrations can be bound as notification targets —
  // data sources (e.g. Freebox) never appear here. This is the visible half of
  // the silent-no-op bug fix.
  const list = (connections ?? []).filter((c) => canNotify(c.type));
  // Disabled channels stay listed if currently bound so the user can unbind
  // them; otherwise they're hidden from the picker.
  const visible = list.filter((c) => c.enabled || selected.includes(c.uid));

  if (visible.length === 0) {
    return (
      <div className="space-y-2">
        <Label>Notify via</Label>
        <div className="rounded border border-dashed p-3 text-sm text-muted-foreground">
          No channels yet.{" "}
          <Link
            to="/orgs/$org/integrations/new"
            params={{ org }}
            className="font-medium text-primary underline-offset-4 hover:underline"
          >
            Create one
          </Link>{" "}
          to be paged when this check fails.
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-2">
      <Label>Notify via</Label>
      <div className="grid gap-2 sm:grid-cols-2">
        {visible.map((c) => {
          const checked = selected.includes(c.uid);
          const cbId = `notify-via-${c.uid}`;
          return (
            <label
              key={c.uid}
              htmlFor={cbId}
              className="flex items-center gap-2 rounded-md border p-2 cursor-pointer hover:bg-muted/50"
            >
              <Checkbox id={cbId} checked={checked} onCheckedChange={() => onToggle(c.uid)} />
              <IntegrationIcon type={c.type} className="h-4 w-4 text-muted-foreground" />
              <span className="text-sm flex-1 truncate">{c.name}</span>
              <span className="text-xs text-muted-foreground">{integrationLabel(c.type)}</span>
              {!c.enabled && (
                <Badge variant="outline" className="text-xs">disabled</Badge>
              )}
            </label>
          );
        })}
      </div>
      <p className="text-xs text-muted-foreground">
        Channels selected here are notified on incident events.{" "}
        <Link
          to="/orgs/$org/integrations"
          params={{ org }}
          className="text-primary underline-offset-4 hover:underline"
        >
          Manage channels
        </Link>
      </p>
    </div>
  );
}
