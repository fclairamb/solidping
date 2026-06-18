import { useState, useMemo, useRef, useEffect } from "react";
import { useTranslation } from "react-i18next";
import { ArrowLeft, Loader2, ChevronsUpDown, Check, Search, Plus, Trash2 } from "lucide-react";
import CodeMirror from "@uiw/react-codemirror";
import { javascript } from "@codemirror/lang-javascript";
import { useCheckValidation, getFieldError } from "@/hooks/use-check-validation";
import { cn } from "@/lib/utils";
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
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Checkbox } from "@/components/ui/checkbox";
import { Switch } from "@/components/ui/switch";
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
import { FreeboxLanDiscovery } from "@/components/shared/freebox-lan-discovery";
import type { FreeboxLanHost } from "@/api/hooks";
import { X } from "lucide-react";

type CheckType = "http" | "tcp" | "icmp" | "dns" | "ssl" | "heartbeat" | "email" | "domain" | "smtp" | "udp" | "ssh" | "pop3" | "imap" | "websocket" | "postgresql" | "mysql" | "redis" | "mongodb" | "ftp" | "sftp" | "js" | "mssql" | "oracle" | "grpc" | "kafka" | "mqtt" | "a2s" | "minecraft" | "rabbitmq" | "snmp" | "docker" | "browser" | "freebox_line" | "dnsbl" | "sip";

// Fallback defaults when API data isn't available
const defaultPeriodSeconds: Record<string, number> = {
  domain: 86400,
  ssl: 21600,
  dns: 300,
};
const globalDefaultPeriodSeconds = 60;
const globalMinPeriodSeconds = 5;

const checkTypes: { value: CheckType; label: string; description: string }[] = [
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
  maxAdaptiveIncrease?: number | null;
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
  const [thresholdDays, setThresholdDays] = useState(
    getConfigField(initialData?.config, "thresholdDays") ||
      getConfigField(initialData?.config, "threshold_days"),
  );
  const [serverName, setServerName] = useState(
    getConfigField(initialData?.config, "serverName") ||
      getConfigField(initialData?.config, "server_name"),
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
  const [maxAdaptiveIncrease, setMaxAdaptiveIncrease] = useState(initialData?.maxAdaptiveIncrease?.toString() ?? "");
  const [confirmationPeriodSeconds, setConfirmationPeriodSeconds] = useState(
    initialData?.confirmationPeriodSeconds?.toString() ?? "",
  );
  const [recoveryPeriodSeconds, setRecoveryPeriodSeconds] = useState(
    initialData?.recoveryPeriodSeconds?.toString() ?? "",
  );
  const [error, setError] = useState<string | null>(null);

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

  // Interval options filtered by type constraints
  const intervalOptions = useMemo(() => {
    const { minSec, maxSec } = getPeriodConstraints(type);
    return buildIntervalOptions(minSec, maxSec);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [type, checkTypeInfoMap]);

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
    setSipTransport(getConfigField(cfg, "transport") || "udp");
    setSipMode(getConfigField(cfg, "mode") || "options");
    setSipExpectStatus(getConfigField(cfg, "expect_status"));
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
        if (thresholdDays) cfg.thresholdDays = parseInt(thresholdDays, 10);
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
    }
    return cfg;
  }, [type, url, host, port, domain, method, expectedStatus, username, password, secretHeaders,
    startTLS, tlsVerify, ehloDomain, expectGreeting, checkAuth, database, query, script,
    serviceName, tls, brokers, topic, produceTest, minPlayers, maxPlayersField, edition,
    vhost, queue, oid, community, expectedValue, snmpOperator, containerName, containerId,
    waitSelector, keyword, wsSend, wsExpect, serverName, thresholdDays,
    freeboxConnectionUid, freeboxLinkType, freeboxMinSyncRate, freeboxMinSnrDb,
    freeboxMaxAttnDb, freeboxMaxCrcErrors, freeboxMinRxMw, freeboxMaxRxMw,
    dnsblTarget, dnsblBlocklists, dnsblNameserver,
    sipTransport, sipMode, sipExpectStatus]);

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
        if (!url) { setError("URL is required"); return; }
        config.url = url;
        if (method && method !== "GET") config.method = method;
        {
          const statusCode = parseInt(expectedStatus, 10);
          if (!isNaN(statusCode) && statusCode !== 200) config.expectedStatus = statusCode;
        }
        if (username) config.username = username;
        if (password) config.password = password;
        {
          const shMap: Record<string, string> = {};
          for (const { key, value } of secretHeaders) {
            if (key) shMap[key] = value;
          }
          if (Object.keys(shMap).length > 0) config.secretHeaders = shMap;
          else config.secretHeaders = {};
        }
        break;
      case "websocket":
        if (!url) { setError("URL is required"); return; }
        config.url = url;
        if (wsSend) config.send = wsSend;
        if (wsExpect) config.expect = wsExpect;
        break;
      case "ssl":
        if (!host) { setError("Host is required"); return; }
        config.host = host;
        if (port) config.port = parseInt(port, 10);
        if (serverName) config.serverName = serverName;
        if (thresholdDays) config.thresholdDays = parseInt(thresholdDays, 10);
        break;
      case "tcp":
      case "udp":
      case "ftp":
        if (!host) { setError("Host is required"); return; }
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
      case "sftp":
        if (!host) { setError("Host is required"); return; }
        config.host = host;
        if (port) config.port = parseInt(port, 10);
        if (username) config.username = username;
        if (password) config.password = password;
        break;
      case "icmp":
        if (!host) { setError("Host is required"); return; }
        config.host = host;
        break;
      case "dns":
      case "domain":
        if (!domain) { setError("Domain is required"); return; }
        config.domain = domain;
        break;
      case "smtp":
        if (!host) { setError("Host is required"); return; }
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
      case "mysql":
      case "mssql":
      case "oracle":
        if (!host) { setError("Host is required"); return; }
        config.host = host;
        if (port) config.port = parseInt(port, 10);
        if (username) config.username = username;
        if (password) config.password = password;
        if (database) config.database = database;
        if (query) config.query = query;
        break;
      case "redis":
        if (!host) { setError("Host is required"); return; }
        config.host = host;
        if (port) config.port = parseInt(port, 10);
        if (password) config.password = password;
        if (database) config.database = parseInt(database, 10);
        break;
      case "mongodb":
        if (!host) { setError("Host is required"); return; }
        config.host = host;
        if (port) config.port = parseInt(port, 10);
        if (username) config.username = username;
        if (password) config.password = password;
        if (database) config.database = database;
        break;
      case "rabbitmq":
        if (!host) { setError("Host is required"); return; }
        config.host = host;
        if (port) config.port = parseInt(port, 10);
        if (username) config.username = username;
        if (password) config.password = password;
        if (vhost) config.vhost = vhost;
        if (queue) config.queue = queue;
        if (tlsVerify) config.tls = true;
        break;
      case "js":
        if (!script) { setError("Script is required"); return; }
        config.script = script;
        break;
      case "grpc":
        if (!host) { setError("Host is required"); return; }
        config.host = host;
        if (port) config.port = parseInt(port, 10);
        if (serviceName) config.serviceName = serviceName;
        if (tls) config.tls = true;
        break;
      case "kafka":
        if (!brokers) { setError("Brokers are required"); return; }
        config.brokers = brokers.split(",").map((b) => b.trim()).filter(Boolean);
        if (topic) config.topic = topic;
        if (username) config.saslUsername = username;
        if (password) config.saslPassword = password;
        if (username || password) config.saslMechanism = "PLAIN";
        if (tls) config.tls = true;
        if (produceTest) config.produceTest = true;
        break;
      case "mqtt":
        if (!host) { setError("Host is required"); return; }
        config.host = host;
        if (port) config.port = parseInt(port, 10);
        if (username) config.username = username;
        if (password) config.password = password;
        if (topic) config.topic = topic;
        if (tls) config.tls = true;
        break;
      case "a2s":
        if (!host) { setError("Host is required"); return; }
        config.host = host;
        if (port) config.port = parseInt(port, 10);
        if (minPlayers) config.minPlayers = parseInt(minPlayers, 10);
        if (maxPlayersField) config.maxPlayers = parseInt(maxPlayersField, 10);
        break;
      case "minecraft":
        if (!host) { setError("Host is required"); return; }
        config.host = host;
        if (port) config.port = parseInt(port, 10);
        if (edition && edition !== "java") config.edition = edition;
        if (minPlayers) config.minPlayers = parseInt(minPlayers, 10);
        if (maxPlayersField) config.maxPlayers = parseInt(maxPlayersField, 10);
        break;
      case "snmp":
        if (!host) { setError("Host is required"); return; }
        config.host = host;
        if (port) config.port = parseInt(port, 10);
        if (oid) config.oid = oid;
        if (community) config.community = community;
        if (expectedValue) config.expectedValue = expectedValue;
        if (snmpOperator && snmpOperator !== "equals") config.operator = snmpOperator;
        break;
      case "docker":
        if (!containerName && !containerId) { setError("Container name or ID is required"); return; }
        if (containerName) config.containerName = containerName;
        if (containerId) config.containerId = containerId;
        if (host) config.host = host;
        break;
      case "browser":
        if (!url) { setError("URL is required"); return; }
        config.url = url;
        if (waitSelector) config.waitSelector = waitSelector;
        if (keyword) config.keyword = keyword;
        break;
      case "freebox_line":
        if (!freeboxConnectionUid) { setError("Freebox connection is required"); return; }
        if (freeboxLinkType !== "xdsl" && freeboxLinkType !== "ftth") {
          setError("Link type must be xdsl or ftth"); return;
        }
        config.connectionUid = freeboxConnectionUid;
        config.linkType = freeboxLinkType;
        if (freeboxMinSyncRate) config.minSyncRateDownKbps = parseInt(freeboxMinSyncRate, 10);
        if (freeboxMinSnrDb) config.minSnrMarginDownDb = parseInt(freeboxMinSnrDb, 10);
        if (freeboxMaxAttnDb) config.maxAttenuationDb = parseInt(freeboxMaxAttnDb, 10);
        if (freeboxMaxCrcErrors) config.maxCrcErrorsPerRun = parseInt(freeboxMaxCrcErrors, 10);
        if (freeboxMinRxMw) config.minRxPowerMw = parseFloat(freeboxMinRxMw);
        if (freeboxMaxRxMw) config.maxRxPowerMw = parseFloat(freeboxMaxRxMw);
        break;
      case "dnsbl":
        if (!dnsblTarget) { setError("Target IP or hostname is required"); return; }
        config.target = dnsblTarget;
        {
          const zones = splitBlocklists(dnsblBlocklists);
          if (zones.length > 0) config.blocklists = zones;
        }
        if (dnsblNameserver) config.nameserver = dnsblNameserver;
        break;
      case "sip":
        if (!host) { setError("Host is required"); return; }
        config.host = host;
        if (port) config.port = parseInt(port, 10);
        if (sipTransport && sipTransport !== "udp") config.transport = sipTransport;
        if (sipMode && sipMode !== "options") config.mode = sipMode;
        if (domain) config.domain = domain;
        if (sipMode === "register") {
          if (!username) { setError("Username is required for register mode"); return; }
          // Allow an empty password on edit when one is already stored (encrypted).
          if (!password && !initialData?.configPrivateKeys?.includes("password")) {
            setError("Password is required for register mode"); return;
          }
          if (username) config.username = username;
          if (password) config.password = password;
        } else if (sipExpectStatus) {
          config.expect_status = sipExpectStatus;
        }
        break;
      case "heartbeat":
      case "email":
        // No config fields needed - token is auto-generated by the backend
        break;
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
        maxAdaptiveIncrease: maxAdaptiveIncrease !== "" ? parseInt(maxAdaptiveIncrease, 10) : null,
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
            <div className="flex gap-4">
              <div className="space-y-2 flex-1">
                <Label htmlFor="serverName">Server Name (SNI, optional)</Label>
                <Input id="serverName" type="text" placeholder="defaults to host" value={serverName} onChange={(e) => setServerName(e.target.value)} data-testid="check-server-name-input" />
              </div>
              <div className="space-y-2 w-40">
                <Label htmlFor="thresholdDays">Threshold (days)</Label>
                <Input id="thresholdDays" type="number" placeholder="30" value={thresholdDays} onChange={(e) => setThresholdDays(e.target.value)} data-testid="check-threshold-days-input" />
              </div>
            </div>
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
      case "pop3":
      case "imap":
        return (
          <>
            <div className="space-y-2">
              <Label>Host</Label>
              <div className="flex gap-2">
                <Input id="host" type="text" placeholder={type === "ssh" ? "server.example.com" : "mail.example.com"} value={host} onChange={(e) => setHost(e.target.value)}
                  className={cn("flex-1", getFieldError(fieldErrors, "host") && "border-destructive")} data-testid="check-host-input" />
                <Input id="port" type="number" placeholder={type === "ssh" ? "22" : type === "pop3" ? "110" : "143"} value={port} onChange={(e) => setPort(e.target.value)}
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
            <CardTitle>Check Type</CardTitle>
            <CardDescription>Select the type of monitoring check</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            {error && (
              <Alert variant="destructive">
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}

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
                                <div className="font-medium">{ct.label}</div>
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
          </CardContent>

          {/* Protocol-specific config */}
          <CardHeader className="pt-2 pb-2">
            <CardTitle className="text-base">Configuration</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            {renderConfigFields()}
          </CardContent>

          {/* Common parameters */}
          <CardHeader className="pt-2 pb-2">
            <CardTitle className="text-base">General</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="flex items-center justify-between">
              <div className="space-y-0.5">
                <Label htmlFor="check-enabled">Enabled</Label>
                <p className="text-xs text-muted-foreground">
                  Disabled checks are not scheduled and never run.
                </p>
              </div>
              <Switch
                id="check-enabled"
                checked={enabled}
                onCheckedChange={setEnabled}
                data-testid="check-enabled-switch"
              />
            </div>

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

            <div className="space-y-2">
              <Label htmlFor="name">Name {mode === "create" && "(optional)"}</Label>
              <Input id="name" type="text" placeholder="My Check" value={name} onChange={(e) => setName(e.target.value)} data-testid="check-name-input" />
              {mode === "create" && (<p className="text-xs text-muted-foreground">If not provided, a name will be auto-generated</p>)}
            </div>

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

            <NotifyViaSection
              org={org}
              connections={connections}
              selected={connectionUids ?? []}
              onToggle={toggleConnection}
            />

            <DependsOnFormSection
              org={org}
              checkUid={mode === "edit" ? initialData?.uid : undefined}
              parents={dependsOnParents ?? []}
              onAdd={addParent}
              onRemove={removeParent}
            />

            {checkGroups && checkGroups.length > 0 && (
              <div className="space-y-2">
                <Label htmlFor="group">Group (optional)</Label>
                <Select value={checkGroupUid || "none"} onValueChange={(v) => setCheckGroupUid(v === "none" ? "" : v)}>
                  <SelectTrigger data-testid="check-group-select"><SelectValue placeholder="No group" /></SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">No group</SelectItem>
                    {checkGroups.map((g) => (<SelectItem key={g.uid} value={g.uid}>{g.name}</SelectItem>))}
                  </SelectContent>
                </Select>
              </div>
            )}

            <div className="space-y-2">
              <Label className="text-base font-medium">Incident Tracking</Label>
              <div className="grid grid-cols-2 gap-4">
                <div className="space-y-1">
                  <Label htmlFor="confirmationPeriodSeconds" className="text-sm">{t("form.confirmationPeriod")}</Label>
                  <Input id="confirmationPeriodSeconds" type="number" min={0} max={86400} placeholder="120 (default)" value={confirmationPeriodSeconds} onChange={(e) => setConfirmationPeriodSeconds(e.target.value)} />
                  <p className="text-xs text-muted-foreground">{t("form.confirmationPeriodHelp")}</p>
                </div>
                <div className="space-y-1">
                  <Label htmlFor="recoveryPeriodSeconds" className="text-sm">{t("form.recoveryPeriod")}</Label>
                  <Input id="recoveryPeriodSeconds" type="number" min={0} max={86400} placeholder="120 (default)" value={recoveryPeriodSeconds} onChange={(e) => setRecoveryPeriodSeconds(e.target.value)} />
                  <p className="text-xs text-muted-foreground">{t("form.recoveryPeriodHelp")}</p>
                </div>
              </div>
            </div>

            <div className="space-y-2">
              <Label className="text-base font-medium">Adaptive Resolution</Label>
              <div className="grid grid-cols-2 gap-4">
                <div className="space-y-1">
                  <Label htmlFor="reopenCooldownMultiplier" className="text-sm">Reopen cooldown multiplier</Label>
                  <Input id="reopenCooldownMultiplier" type="number" min={0} placeholder="5 (default)" value={reopenCooldownMultiplier} onChange={(e) => setReopenCooldownMultiplier(e.target.value)} />
                  <p className="text-xs text-muted-foreground">How many check periods to wait before closing a resolved incident. 0 = never reopen.</p>
                </div>
                <div className="space-y-1">
                  <Label htmlFor="maxAdaptiveIncrease" className="text-sm">Max adaptive increase</Label>
                  <Input id="maxAdaptiveIncrease" type="number" min={0} placeholder="5 (default)" value={maxAdaptiveIncrease} onChange={(e) => setMaxAdaptiveIncrease(e.target.value)} />
                  <p className="text-xs text-muted-foreground">Reserved for the reopen-cooldown calculation. 0 = no extra wait.</p>
                </div>
              </div>
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
          <CardFooter className="flex justify-end gap-2">
            <Button type="button" variant="outline" onClick={onCancel}>Cancel</Button>
            <Button type="submit" disabled={isPending} data-testid="check-submit-button">
              {isPending ? (<><Loader2 className="mr-2 h-4 w-4 animate-spin" />{pendingLabel}</>) : submitLabel}
            </Button>
          </CardFooter>
        </form>
      </Card>
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
