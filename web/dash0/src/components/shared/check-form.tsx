import { useState, useMemo, useRef, useEffect } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle, ArrowLeft, Loader2, ChevronsUpDown, Check, Search } from "lucide-react";
import { useCheckValidationResult, getFieldError } from "@/hooks/use-check-validation";
import { cn } from "@/lib/utils";
import { resolveCheckRefLabel } from "@/lib/dependency-graph";
import {
  Ipv6CapabilityBadge,
  ipv6Capability,
  type Ipv6Capability,
} from "@/components/shared/ipv6-capability";
import { describePeriod, formatDuration } from "@/lib/period-estimate";
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
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Checkbox } from "@/components/ui/checkbox";
import { Switch } from "@/components/ui/switch";
import { CollapsibleSection } from "@/components/ui/collapsible-section";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { LabelInput } from "@/components/shared/label-input";
import { DocsLink } from "@/components/shared/docs-link";
import { docsHrefForType } from "@/components/shared/check-type-docs-anchors";
import { CheckTypeIcon } from "@/components/shared/check-type-identity";
import { ApiError } from "@/api/client";
import type { Check as CheckModel, CheckGroup, RegionDefinition, SampleConfig } from "@/api/hooks";
import {
  useChecks,
  useCheckTypes,
  useSampleConfigs,
  useIntegrations,
  useCheckConnections,
  useCheckDependencies,
  canNotify,
} from "@/api/hooks";
import { Badge } from "@/components/ui/badge";
import { NotifyViaSection } from "@/components/checks/form/sections/notifications";
import { EscalationSelect } from "@/components/checks/form/sections/escalation";
import { DependsOnFormSection } from "@/components/checks/form/sections/dependencies";
import { checkTypeRegistry, authFieldsRegistry, advancedFieldsRegistry } from "@/components/checks/form/types";
import { CheckFormFieldsProvider } from "@/components/checks/form/types/context";
import { TunnelSelect } from "@/components/checks/form/tunnel-select";
import {
  IPVersionSelect,
  IP_VERSION_AUTO,
} from "@/components/checks/form/ip-version-select";
import {
  getConfigField,
  durationStringToSeconds,
} from "@/components/checks/form/types/common";
import type { CheckType } from "@/components/checks/form/types/common";

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
  { value: "clickhouse", label: "ClickHouse", description: "Check ClickHouse database health" },
  { value: "grpc", label: "gRPC", description: "Check gRPC service health" },
  { value: "kafka", label: "Kafka", description: "Check Kafka cluster health" },
  { value: "mqtt", label: "MQTT", description: "Check MQTT broker connectivity" },
  { value: "a2s", label: "A2S Game Server", description: "Monitor Source engine game servers via A2S" },
  { value: "minecraft", label: "Minecraft", description: "Monitor Minecraft servers (Java + Bedrock)" },
  { value: "rabbitmq", label: "RabbitMQ", description: "Check RabbitMQ server health" },
  { value: "snmp", label: "SNMP", description: "Monitor devices via SNMP" },
  { value: "prometheus", label: "Prometheus", description: "Alert on Prometheus metric thresholds" },
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

export type PeriodUnit = "minutes" | "hours" | "days" | "weeks";

const periodUnits: { value: PeriodUnit; label: string }[] = [
  { value: "minutes", label: "Minutes" },
  { value: "hours", label: "Hours" },
  { value: "days", label: "Days" },
  { value: "weeks", label: "Weeks" },
];

export function parsePeriod(period: string): { value: number; unit: PeriodUnit } {
  const [h, m, s] = period.split(":").map(Number);
  const totalSeconds = h * 3600 + m * 60 + s;
  if (totalSeconds % (7 * 86400) === 0) return { value: totalSeconds / (7 * 86400), unit: "weeks" };
  if (totalSeconds % 86400 === 0) return { value: totalSeconds / 86400, unit: "days" };
  if (totalSeconds % 3600 === 0) return { value: totalSeconds / 3600, unit: "hours" };
  return { value: Math.max(1, Math.round(totalSeconds / 60)), unit: "minutes" };
}

export function formatPeriod(value: number, unit: PeriodUnit): string {
  const multipliers = { minutes: 60, hours: 3600, days: 86400, weeks: 604800 };
  const totalSeconds = value * multipliers[unit];
  const h = Math.floor(totalSeconds / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  const s = totalSeconds % 60;
  return `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
}

export function secondsToHMS(seconds: number): string {
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = seconds % 60;
  return `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
}

export function hmsToSeconds(hms: string): number {
  const [h, m, s] = hms.split(":").map(Number);
  return h * 3600 + m * 60 + s;
}

// The optional multi-region "Region Spread" override (spec 2026-07-20-05
// backend / 2026-07-21-01 this UI) needs finer granularity than the
// whole-minute period picker — the automatic default can be a handful of
// seconds — so it gets its own number+unit pair down to seconds.
type RegionSpreadUnit = "seconds" | "minutes" | "hours";

const regionSpreadUnits: { value: RegionSpreadUnit; label: string }[] = [
  { value: "seconds", label: "Seconds" },
  { value: "minutes", label: "Minutes" },
  { value: "hours", label: "Hours" },
];

const regionSpreadUnitSeconds: Record<RegionSpreadUnit, number> = {
  seconds: 1,
  minutes: 60,
  hours: 3600,
};

// parseRegionSpread seeds the number+unit input from a stored "HH:MM:SS"
// regionSpread: picks the largest whole unit that divides the value evenly
// (falling back to seconds), so an existing value round-trips without a
// surprising unit jump (e.g. 90s stays "90 seconds", not "1.5 minutes").
function parseRegionSpread(hms: string): { value: string; unit: RegionSpreadUnit } {
  const totalSeconds = hmsToSeconds(hms);
  if (totalSeconds > 0 && totalSeconds % 3600 === 0) {
    return { value: String(totalSeconds / 3600), unit: "hours" };
  }
  if (totalSeconds > 0 && totalSeconds % 60 === 0) {
    return { value: String(totalSeconds / 60), unit: "minutes" };
  }
  return { value: String(totalSeconds), unit: "seconds" };
}

export function buildIntervalOptions(minSeconds: number, maxSeconds: number): { value: string; label: string }[] {
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
    { seconds: 604800, value: "168:00:00", label: "1 week" },
    { seconds: 1209600, value: "336:00:00", label: "2 weeks" },
    { seconds: 2592000, value: "720:00:00", label: "30 days" },
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
  /** "" clears an existing override back to automatic; omit to leave unchanged. */
  regionSpread?: string;
  /** `inherit` | `on` | `off` — the per-check path-trace policy. */
  tracerouteOnFailure?: string;
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
  /** "" = inherit (group → org default → none); a UID assigns that policy. */
  escalationPolicyUid?: string;
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

  const slugRegex = /^[a-z][a-z0-9-]{2,99}$/;
  const uuidRegex = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

  function validateSlug(value: string): string | null {
    if (!value) return null; // empty is OK (auto-generated)
    if (uuidRegex.test(value)) return "Slug must not be a UUID";
    if (!slugRegex.test(value)) {
      if (value.length < 3) return "Slug must be at least 3 characters";
      if (value.length > 100) return "Slug must be at most 100 characters";
      if (!/^[a-z]/.test(value)) return "Slug must start with a lowercase letter";
      return "Slug must contain only lowercase letters, digits, and hyphens";
    }
    return null;
  }

  const [type, setType] = useState<CheckType>(initialType);

  // Whether the selected type can tunnel is server-declared capability metadata
  // — never a hard-coded list here, so a checker gaining tunnel support needs
  // no frontend change.
  const supportsTunnel = checkTypeInfoMap.get(type)?.supportsTunnel === true;

  // Same story for the address family: server-declared capability metadata, so
  // a checker gaining `ipVersion` support needs no frontend change.
  const supportsIpVersion =
    checkTypeInfoMap.get(type)?.supportsIpVersion === true;

  // The org's SSH checks are the tunnel candidates, filtered server-side.
  const { data: sshChecks } = useChecks(org, { type: "ssh", limit: 100 });
  const tunnelCandidates = sshChecks ?? [];
  const [enabled, setEnabled] = useState(initialData?.enabled ?? true);
  const [name, setName] = useState(initialData?.name || "");
  const [slug, setSlug] = useState(initialData?.slug || "");
  const slugError = validateSlug(slug);
  const [checkGroupUid, setCheckGroupUid] = useState(initialData?.checkGroupUid || "");
  // Escalation policy assignment: "" = inherit (group → org default → none),
  // a UID = that policy. PATCH semantics: send the UID to set, "" to clear.
  const [escalationPolicyUid, setEscalationPolicyUid] = useState(
    initialData?.escalationPolicyUid ?? "",
  );
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
          label: resolveCheckRefLabel(e.parentCheck),
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

  // Optional SSH tunnel — the well-known `tunnelCheckUid` config key. Shared and
  // protocol-agnostic like `timeout` above, so it lives here rather than in a
  // type module; empty = direct connection.
  const [tunnelCheckUid, setTunnelCheckUid] = useState(
    getConfigField(initialData?.config, "tunnelCheckUid"),
  );

  // Optional address family — the well-known `ipVersion` config key. Shared and
  // protocol-agnostic like `timeout` and `tunnelCheckUid`; empty/"auto" = no
  // constraint (whichever family the target resolves to first).
  const [ipVersion, setIpVersion] = useState(
    getConfigField(initialData?.config, "ipVersion") || IP_VERSION_AUTO,
  );

  // The active check type's config state — one object seeded via the type
  // module's `fromConfig` (spec §3), replacing the ~96 flat per-type useState
  // hooks. Re-seeded on type change so shared fields (host, url, …) carry over.
  const [configState, setConfigState] = useState<unknown>(() =>
    checkTypeRegistry[initialType].fromConfig(initialData?.config ?? {}),
  );

  const [selectedRegions, setSelectedRegions] = useState<string[]>(initialData?.regions ?? defaultRegions ?? []);
  // Region Spread: "" = unset (keep automatic default). Seeded from the
  // stored override, if any — absent means the check uses the automatic
  // period/region-count default.
  const initialRegionSpread = initialData?.regionSpread
    ? parseRegionSpread(initialData.regionSpread)
    : null;
  const [regionSpreadValue, setRegionSpreadValue] = useState(initialRegionSpread?.value ?? "");
  const [regionSpreadUnit, setRegionSpreadUnit] = useState<RegionSpreadUnit>(
    initialRegionSpread?.unit ?? "seconds",
  );
  const [tracerouteOnFailure, setTracerouteOnFailure] = useState(
    initialData?.tracerouteOnFailure ?? "inherit",
  );
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

  // Effective per-region period for the regions hint: since each selected
  // region runs the check at the FULL period (spec 2026-07-20-05), spell that
  // out so users understand multi-region multiplies coverage, not divides it.
  const regionPeriodSeconds = hmsToSeconds(isPassiveType(type) ? formatPeriod(periodValue, periodUnit) : period);

  // Region Spread is only meaningful once 2+ regions are actually selected
  // (a single region has nothing to stagger against) — mirrors the existing
  // regions-hint visibility gate below.
  const hasMultiRegionSpread = showRegions && selectedRegions.length > 1;
  const hasRegionSpreadInput = regionSpreadValue.trim() !== "";
  const regionSpreadSeconds = hasRegionSpreadInput
    ? Math.round(Number(regionSpreadValue) * regionSpreadUnitSeconds[regionSpreadUnit])
    : null;
  const autoRegionSpreadSeconds =
    selectedRegions.length > 0 ? Math.floor(regionPeriodSeconds / selectedRegions.length) : 0;
  // Client-side mirror of the backend bound 0 <= regionSpread < period
  // (service.go's validateRegionSpread) — catches the common mistakes before
  // a round trip; the backend VALIDATION_ERROR is still authoritative and
  // surfaces via the top-level error Alert if this client check is ever wrong
  // (e.g. the period changed elsewhere) or stale.
  const regionSpreadError =
    hasMultiRegionSpread &&
    hasRegionSpreadInput &&
    (regionSpreadSeconds === null ||
      isNaN(regionSpreadSeconds) ||
      regionSpreadSeconds < 0 ||
      regionSpreadSeconds >= regionPeriodSeconds)
      ? t("form.regionSpreadRangeError", {
          period: formatDuration(regionPeriodSeconds),
          defaultValue: `Region spread must be at least 0 and less than the check period (${formatDuration(regionPeriodSeconds)}).`,
        })
      : null;

  // Advertised per-region IPv6 egress (spec 2026-08-15-11). Purely a hint:
  // when the check is pinned to `ipv6` we surface which regions currently say
  // they can do it and float those to the top, but every region stays listed,
  // selectable and submittable — the run-time probe is the authority, and an
  // "unknown" region may well be capable.
  const pinnedIpv6 = supportsIpVersion && ipVersion === "ipv6";
  const orderedRegions = useMemo(() => {
    const list = availableRegions ?? [];

    if (!pinnedIpv6) {
      return list;
    }

    // "yes" first, then "unknown", then "no" — never removing anything. Stable
    // within each bucket so the picker keeps the server's ordering otherwise.
    const rank: Record<Ipv6Capability, number> = { yes: 0, unknown: 1, no: 2 };

    return [...list].sort(
      (a, b) => rank[ipv6Capability(a.capabilities)] - rank[ipv6Capability(b.capabilities)]
    );
  }, [availableRegions, pinnedIpv6]);

  const activeModule = checkTypeRegistry[type];
  const ActiveFields = activeModule.Fields;
  const authSection = authFieldsRegistry[type];
  const AuthFields = authSection?.Fields;
  const advancedSection = advancedFieldsRegistry[type];
  const AdvancedTypeFields = advancedSection?.Fields;
  const advancedTypeSummary = advancedSection?.summary(configState);

  // Serialize the active type's config ONCE (spec §3): the same object feeds the
  // live preview/validation AND the submitted payload, so they cannot drift. The
  // shared, protocol-agnostic per-check timeout is layered on afterwards.
  const serialized = useMemo(
    () => activeModule.toConfig(configState),
    [activeModule, configState],
  );
  const currentConfig = useMemo(() => {
    const cfg: Record<string, unknown> = { ...serialized.config };
    if (!isPassiveType(type) && timeoutSeconds !== "") {
      const tv = parseInt(timeoutSeconds, 10);
      if (!isNaN(tv)) cfg.timeout = `${tv}s`;
    }
    // Only tunnel-capable types carry the key; switching to a type that cannot
    // tunnel drops it rather than submitting a config the server would reject.
    if (supportsTunnel && tunnelCheckUid !== "") {
      cfg.tunnelCheckUid = tunnelCheckUid;
    }
    // Only pinned families are written: `auto` is the default, and a tunneled
    // check may not carry the key at all (the server rejects the pair), so a
    // stale selection never leaks into the submitted config.
    if (
      supportsIpVersion &&
      ipVersion !== "" &&
      ipVersion !== IP_VERSION_AUTO &&
      !(supportsTunnel && tunnelCheckUid !== "")
    ) {
      cfg.ipVersion = ipVersion;
    }
    return cfg;
  }, [
    serialized,
    type,
    timeoutSeconds,
    supportsTunnel,
    tunnelCheckUid,
    supportsIpVersion,
    ipVersion,
  ]);

  const { errors: fieldErrors, warnings: fieldWarnings } = useCheckValidationResult(
    org,
    type,
    currentConfig,
    selectedRegions,
    300,
  );

  const toggleRegion = (slug: string) => {
    setSelectedRegions((prev) =>
      prev.includes(slug) ? prev.filter((r) => r !== slug) : [...prev, slug]
    );
  };

  // Apply a sample config: seed the active type's module state plus the shared
  // name/slug/period/timeout.
  function applySample(sample: SampleConfig) {
    setName(sample.name);
    setSlug(sample.slug);
    setPeriod(secondsToHMS(sample.periodSeconds));
    setConfigState(checkTypeRegistry[type].fromConfig(sample.config));
    setTimeoutSeconds(durationStringToSeconds(getConfigField(sample.config, "timeout")));
    setTunnelCheckUid(getConfigField(sample.config, "tunnelCheckUid"));
    setIpVersion(getConfigField(sample.config, "ipVersion") || IP_VERSION_AUTO);
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSubmitAttempts((n) => n + 1);

    // Reuse `currentConfig` (the same object the live preview and validation use)
    // as the submitted payload, so preview and payload can never drift apart.
    // Secret sections decide for themselves whether to serialize their keys
    // (see the dirty flags in the http module): an omitted key preserves the
    // stored value, an explicit empty one clears it. Forcing `secretHeaders:{}`
    // here used to wipe them on every edit, since GET never returns them.
    const config: Record<string, unknown> = { ...currentConfig };

    // Required-field validation is the module's `toConfig` output. A field whose
    // value is stored server-side (listed in configPrivateKeys) is already
    // satisfied — drop its "required" error (e.g. a SIP register password kept
    // encrypted on edit).
    const blockingErrors = serialized.errors.filter(
      (fe) => !initialData?.configPrivateKeys?.includes(fe.name),
    );
    if (blockingErrors.length > 0) {
      setError(blockingErrors[0].message);
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

    // Region spread: block on the same client-side mirror shown inline under
    // the field, so an out-of-range value never reaches the server.
    if (regionSpreadError) {
      setError(regionSpreadError);
      return;
    }

    try {
      await onSubmit({
        type: mode === "create" ? type : undefined,
        enabled,
        name: mode === "edit" ? name : (name || undefined),
        slug: mode === "edit" ? slug : (slug || undefined),
        checkGroupUid: checkGroupUid || (mode === "edit" ? "" : undefined),
        // Inherit ("" state) sends "" on edit (clears any prior assignment) and
        // nothing on create (the check inherits by default).
        escalationPolicyUid:
          escalationPolicyUid || (mode === "edit" ? "" : undefined),
        period: isPassiveType(type) ? formatPeriod(periodValue, periodUnit) : period,
        // Don't send config for passive edits — the token is managed by the backend
        ...(isPassiveType(type) && mode === "edit" ? {} : { config }),
        ...(showRegions ? { regions: selectedRegions } : {}),
        // Mirrors the checkGroupUid/escalationPolicyUid PATCH idiom: a
        // duration string sets the override, "" clears it back to automatic
        // on edit, and the key is omitted (untouched) whenever the field
        // isn't visible (< 2 regions) or create mode leaves it unset — never
        // send `0` for "unset" (0 is a valid explicit spread).
        ...(hasMultiRegionSpread
          ? {
              regionSpread: hasRegionSpreadInput
                ? secondsToHMS(regionSpreadSeconds ?? 0)
                : (mode === "edit" ? "" : undefined),
            }
          : {}),
        // Always sent, including "inherit": it is the only way to put a check
        // that carries an explicit on/off back under the org default, and an
        // omitted field means "leave unchanged" on PATCH.
        tracerouteOnFailure,
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

  const isEdit = mode === "edit";
  const title = isEdit ? "Edit Check" : "New Check";
  const subtitle = isEdit ? "Update the monitoring check parameters" : "Create a new monitoring check";
  const submitLabel = isEdit ? "Save Changes" : "Create Check";
  const pendingLabel = isEdit ? "Saving..." : "Creating...";

  const selectedTypeLabel = checkTypes.find((t) => t.value === type)?.label || type;

  // ── Progressive-disclosure section summaries + open-on-content ──
  const authSummary = authSection
    ? authSection.summary(configState, initialData?.configPrivateKeys)
    : { text: "", customized: false };

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
  const advancedCustomized =
    timeoutSeconds.trim() !== "" ||
    (supportsTunnel && tunnelCheckUid !== "") ||
    (supportsIpVersion && ipVersion !== IP_VERSION_AUTO && ipVersion !== "") ||
    !!advancedTypeSummary?.customized;
  const advancedSummary = [
    timeoutSeconds.trim() !== "" ? `timeout ${timeoutSeconds}s` : "",
    advancedTypeSummary?.customized ? advancedTypeSummary.text : "",
  ]
    .filter(Boolean)
    .join(" · ") || "timeout 15s (default)";
  const showGroup = (checkGroups?.length ?? 0) > 0;

  // A section opens on load when it holds non-default values OR is the target of
  // a `?section=<id>` deep-link (which the mount effect above also scrolls to).
  const sectionOpen = (id: string, hasValues: boolean) =>
    hasValues || initialSection === id;

  return (
    <CheckFormFieldsProvider
      value={{
        type,
        org,
        connections,
        configPrivateKeys: initialData?.configPrivateKeys,
        name,
        setName,
      }}
    >
      <div className="space-y-6 max-w-2xl">
        <div className="flex items-center gap-4">
          <Button variant="ghost" size="icon" onClick={onCancel}>
            <ArrowLeft className="h-4 w-4" />
          </Button>
          <div className="min-w-0 flex-1">
            <h1 className="text-3xl font-bold tracking-tight">{title}</h1>
            <p className="text-muted-foreground">{subtitle}</p>
          </div>
          <DocsLink href={docsHrefForType(type)} className="ml-auto" />
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
                                  // Re-seed the active module's state, carrying
                                  // over shared fields (host, url, …) from the
                                  // previously-serialized config.
                                  setConfigState(
                                    checkTypeRegistry[newType].fromConfig(currentConfig),
                                  );
                                  setType(newType);
                                  setPeriod(getDefaultPeriodHMS(newType));
                                  onTypeChange?.(newType);
                                  setTypeSearchOpen(false);
                                  setTypeSearch("");
                                }}
                              >
                                <Check className={cn("mt-0.5 h-4 w-4 shrink-0", type === ct.value ? "opacity-100" : "opacity-0")} />
                                <CheckTypeIcon type={ct.value} className="mt-0.5" />
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
              <ActiveFields
                state={configState}
                onChange={setConfigState}
                errors={fieldErrors}
              />

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
                    {orderedRegions.map((region) => {
                      const ipv6 = ipv6Capability(region.capabilities);
                      // De-emphasise only when the check is pinned to ipv6 and
                      // the region does not advertise it. Never disabled, never
                      // hidden — the advertised value is a hint, not a gate.
                      const deemphasised = pinnedIpv6 && ipv6 !== "yes";

                      return (
                        <label
                          key={region.slug}
                          className={cn(
                            "flex flex-wrap items-center gap-2 rounded-md border p-2 cursor-pointer hover:bg-muted/50",
                            deemphasised && "opacity-60"
                          )}
                          data-testid={`region-option-${region.slug}`}
                          data-ipv6={ipv6}
                        >
                          <Checkbox checked={selectedRegions.includes(region.slug)} onCheckedChange={() => toggleRegion(region.slug)} />
                          <span className="text-sm">{region.emoji} {region.name}</span>
                          <span className="ml-auto flex items-center gap-1">
                            {region.private && (
                              <Badge variant="secondary" className="text-[10px]" title="Runs on your own deported agents">
                                Private
                              </Badge>
                            )}
                            {/* "yes" is always marked; "no" is always shown so
                                its absence can never be misread. "unknown" gets
                                a neutral badge only while ipv6 is pinned, where
                                the distinction actually matters. */}
                            <Ipv6CapabilityBadge
                              capability={ipv6}
                              hideUnknown={!pinnedIpv6}
                              className="text-[10px]"
                              data-testid={`region-ipv6-${region.slug}`}
                            />
                          </span>
                        </label>
                      );
                    })}
                  </div>
                  <p className="text-xs text-muted-foreground">Select the regions where this check should run</p>
                  {/* Advisory only (spec 2026-08-19-03): a browser check whose
                      selected regions report no headless Chrome. Never blocks
                      submit — the advertised value lags by a heartbeat, and
                      "unknown" never warns at all. */}
                  {getFieldError(fieldWarnings, "regions") && (
                    <Alert
                      variant="warning"
                      className="mt-2"
                      data-testid="check-regions-warning"
                    >
                      <AlertTriangle />
                      <AlertDescription>
                        {getFieldError(fieldWarnings, "regions")}
                      </AlertDescription>
                    </Alert>
                  )}
                  {pinnedIpv6 && (
                    <p className="text-xs text-muted-foreground" data-testid="regions-ipv6-hint">
                      {t("form.regionsIpv6Hint", {
                        defaultValue:
                          "This check is pinned to IPv6. Regions whose live workers report IPv6 egress are listed first — the others stay selectable, and a region marked “IPv6 unknown” has simply not reported yet.",
                      })}
                    </p>
                  )}
                  {selectedRegions.length > 1 && regionPeriodSeconds > 0 && (
                    <p className="text-xs text-muted-foreground" data-testid="regions-period-hint">
                      {hasRegionSpreadInput && !regionSpreadError
                        ? t("form.regionsHintSpread", {
                            period: formatDuration(regionPeriodSeconds),
                            spread: formatDuration(regionSpreadSeconds ?? 0),
                            defaultValue:
                              "Each selected region runs the check every {{period}}, staggered {{spread}} apart.",
                          })
                        : t("form.regionsHint", {
                            period: formatDuration(regionPeriodSeconds),
                            defaultValue: "Each selected region runs the check every {{period}}.",
                          })}
                    </p>
                  )}

                  {/* Advanced/secondary override — only relevant once there is
                      more than one region to stagger. Default path (nothing
                      typed) stays zero-config: the backend already spreads
                      regions evenly across the period. */}
                  {hasMultiRegionSpread && (
                    <div className="space-y-1.5 rounded-md border border-dashed p-3">
                      <Label htmlFor="regionSpread" className="text-sm">
                        {t("form.regionSpread", { defaultValue: "Region Spread" })}
                      </Label>
                      <div className="flex flex-wrap gap-2">
                        <Input
                          id="regionSpread"
                          type="number"
                          min={0}
                          step={1}
                          placeholder={String(autoRegionSpreadSeconds)}
                          value={regionSpreadValue}
                          onChange={(e) => setRegionSpreadValue(e.target.value)}
                          className={cn("w-24", regionSpreadError && "border-destructive")}
                          data-testid="check-region-spread-input"
                        />
                        <Select
                          value={regionSpreadUnit}
                          onValueChange={(v) => setRegionSpreadUnit(v as RegionSpreadUnit)}
                        >
                          <SelectTrigger
                            data-testid="check-region-spread-unit-select"
                            className="min-w-[8rem] flex-1"
                          >
                            <SelectValue />
                          </SelectTrigger>
                          <SelectContent>
                            {regionSpreadUnits.map((u) => (
                              <SelectItem key={u.value} value={u.value}>{u.label}</SelectItem>
                            ))}
                          </SelectContent>
                        </Select>
                      </div>
                      {regionSpreadError ? (
                        <p className="text-xs text-destructive" data-testid="region-spread-error">
                          {regionSpreadError}
                        </p>
                      ) : (
                        <p className="text-xs text-muted-foreground" data-testid="region-spread-help">
                          {hasRegionSpreadInput
                            ? t("form.regionSpreadHelp", {
                                defaultValue:
                                  "Overrides how far apart regions are staggered. Leave empty to keep automatic spreading.",
                              })
                            : t("form.regionSpreadAutomatic", {
                                spread: formatDuration(autoRegionSpreadSeconds),
                                count: selectedRegions.length,
                                defaultValue: "Automatic: {{spread}} (period / {{count}} regions)",
                              })}
                        </p>
                      )}
                    </div>
                  )}
                  {/* A region change can be rejected because of an SSH tunnel
                      dependency (the tunnel's SSH check must cover every private
                      region this check runs in). The server reports it on the
                      tunnel field; surface it here too so a region edit that
                      triggered it shows the reason next to the regions. */}
                  {supportsTunnel &&
                    tunnelCheckUid !== "" &&
                    getFieldError(fieldErrors, "tunnelCheckUid") && (
                      <p
                        className="text-xs text-destructive"
                        data-testid="region-tunnel-error"
                      >
                        {getFieldError(fieldErrors, "tunnelCheckUid")}
                      </p>
                    )}
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
              <EscalationSelect
                org={org}
                value={escalationPolicyUid}
                onChange={setEscalationPolicyUid}
                checkGroupUid={checkGroupUid}
                checkGroups={checkGroups}
              />
            </CardContent>
          </Card>

          {/* 4. Collapsible sections — collapsed unless customized */}
          {authSection && AuthFields && (
            <CollapsibleSection
              id="authentication"
              data-testid="section-authentication-trigger"
              title="Authentication & secrets"
              summary={authSummary.text}
              customized={authSummary.customized}
              defaultOpen={sectionOpen("authentication", authSummary.customized)}
            >
              <AuthFields
                state={configState}
                onChange={setConfigState}
                errors={fieldErrors}
              />
            </CollapsibleSection>
          )}

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
              {supportsTunnel && (
                <div className="mt-4" data-testid="check-tunnel-section">
                  <TunnelSelect
                    org={org}
                    sshChecks={tunnelCandidates}
                    selectedRegions={selectedRegions}
                    value={tunnelCheckUid}
                    onChange={setTunnelCheckUid}
                  />
                  {getFieldError(fieldErrors, "tunnelCheckUid") && (
                    <p className="text-xs text-destructive">
                      {getFieldError(fieldErrors, "tunnelCheckUid")}
                    </p>
                  )}
                </div>
              )}
              {supportsIpVersion && (
                <div className="mt-4" data-testid="check-ip-version-section">
                  <IPVersionSelect
                    value={ipVersion}
                    onChange={setIpVersion}
                    tunneled={supportsTunnel && tunnelCheckUid !== ""}
                  />
                  {getFieldError(fieldErrors, "ipVersion") && (
                    <p className="text-xs text-destructive">
                      {getFieldError(fieldErrors, "ipVersion")}
                    </p>
                  )}
                  {/* Advisory only (spec 2026-08-15-11): the selected regions
                      currently report no IPv6 egress. It never blocks submit —
                      the advertised value lags, and the run-time probe is the
                      authority. */}
                  {getFieldError(fieldWarnings, "ipVersion") && (
                    <Alert
                      variant="warning"
                      className="mt-2"
                      data-testid="check-ip-version-warning"
                    >
                      <AlertTriangle />
                      <AlertDescription>
                        {getFieldError(fieldWarnings, "ipVersion")}
                      </AlertDescription>
                    </Alert>
                  )}
                </div>
              )}
              <div className="mt-4 space-y-2" data-testid="check-traceroute-section">
                <Label htmlFor="check-traceroute">
                  {t("form.tracerouteOnFailure")}
                </Label>
                <Select
                  value={tracerouteOnFailure}
                  onValueChange={setTracerouteOnFailure}
                >
                  <SelectTrigger
                    id="check-traceroute"
                    data-testid="check-traceroute-select"
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="inherit">
                      {t("form.tracerouteInherit")}
                    </SelectItem>
                    <SelectItem value="on">{t("form.tracerouteOn")}</SelectItem>
                    <SelectItem value="off">
                      {t("form.tracerouteOff")}
                    </SelectItem>
                  </SelectContent>
                </Select>
                <p className="text-xs text-muted-foreground">
                  {t("form.tracerouteOnFailureHelp")}
                </p>
              </div>
              {AdvancedTypeFields && (
                <div className="mt-4">
                  <AdvancedTypeFields
                    state={configState}
                    onChange={setConfigState}
                    errors={fieldErrors}
                  />
                </div>
              )}
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
    </CheckFormFieldsProvider>
  );
}
