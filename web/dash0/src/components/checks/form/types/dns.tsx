import { useTranslation } from "react-i18next";
import { cn } from "@/lib/utils";
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
import { getFieldError } from "@/hooks/use-check-validation";
import { TokenChipsInput } from "@/components/shared/token-chips-input";
import type { CheckTypeModule } from "./index";
import type { CheckConfig, CheckTypeFieldsProps, FieldErrors } from "./common";
import { getConfigField, splitBlocklists } from "./common";

// ── DNS ──
// The queried domain is bound to the backend `host` key (label stays "Domain").
export interface DnsState {
  host: string;
  nameserver: string;
  recordType: string;
  // Chips bound to `expected_ips` (A/AAAA answers). Kept separate from
  // `expectedValues` because the backend rejects a config carrying both keys —
  // toConfig only writes the one matching the current record type.
  expectedIps: string[];
  // Textarea (one entry per line) bound to `expected_values` (CNAME/MX/NS/TXT
  // answers). A textarea rather than chips because TXT values legitimately
  // contain spaces (e.g. an SPF record), which a chip input would split on.
  expectedValues: string;
}

// A/AAAA lookups resolve to IPs and assert against `expected_ips`; every other
// record type resolves to strings and asserts against `expected_values`.
export function isIpRecordType(recordType: string): boolean {
  return recordType === "A" || recordType === "AAAA";
}

export function isValidIPv4(token: string): boolean {
  const parts = token.split(".");
  return (
    parts.length === 4 &&
    parts.every((p) => /^\d{1,3}$/.test(p) && Number(p) <= 255)
  );
}

export function isValidIPv6(token: string): boolean {
  if (!/^[0-9a-f:]+$/i.test(token) || !token.includes(":")) return false;
  const halves = token.split("::");
  if (halves.length > 2) return false;
  const groups = token.split(/::?/).filter(Boolean);
  if (groups.length > 8) return false;
  // Without "::" compression, exactly 8 groups are required.
  if (halves.length === 1 && groups.length !== 8) return false;
  return groups.every((g) => /^[0-9a-f]{1,4}$/i.test(g));
}

function seedStringArray(config: CheckConfig, field: string): string[] {
  const raw = config[field];
  if (!Array.isArray(raw)) return [];
  return raw.map(String).filter((s) => s.trim() !== "");
}

export const dnsModule: CheckTypeModule<DnsState> = {
  types: ["dns"],
  fromConfig: (config) => ({
    host: getConfigField(config, "host"),
    nameserver: getConfigField(config, "nameserver"),
    recordType: getConfigField(config, "record_type") || "A",
    expectedIps: seedStringArray(config, "expected_ips"),
    expectedValues: seedStringArray(config, "expected_values").join("\n"),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.host) cfg.host = state.host;
    if (state.nameserver) cfg.nameserver = state.nameserver;
    if (state.recordType && state.recordType !== "A")
      cfg.record_type = state.recordType;
    const errors: FieldErrors = state.host
      ? []
      : [{ name: "host", message: "Domain is required" }];
    // Only the expectation matching the record type is written; the other is
    // dropped so a record-type change never produces the (rejected)
    // both-keys config, or an assertion that can never match.
    if (isIpRecordType(state.recordType || "A")) {
      if (state.expectedIps.length > 0) cfg.expected_ips = state.expectedIps;
      const validate = state.recordType === "AAAA" ? isValidIPv6 : isValidIPv4;
      const invalid = state.expectedIps.filter((ip) => !validate(ip));
      if (invalid.length > 0) {
        errors.push({
          name: "expected_ips",
          message: `Not ${state.recordType === "AAAA" ? "IPv6" : "IPv4"} address${invalid.length > 1 ? "es" : ""}: ${invalid.join(", ")}`,
        });
      }
    } else {
      const values = splitBlocklists(state.expectedValues);
      if (values.length > 0) cfg.expected_values = values;
    }
    return { config: cfg, errors };
  },
  Fields: DnsFields,
};

function DnsFields({ state, onChange, errors }: CheckTypeFieldsProps<DnsState>) {
  const { t } = useTranslation("checks");
  const ipMode = isIpRecordType(state.recordType || "A");
  const validateIp = state.recordType === "AAAA" ? isValidIPv6 : isValidIPv4;
  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="domain">Domain</Label>
        <Input
          id="domain"
          type="text"
          placeholder="example.com"
          value={state.host}
          onChange={(e) => onChange({ ...state, host: e.target.value })}
          className={cn(getFieldError(errors, "host") && "border-destructive")}
          data-testid="check-domain-input"
        />
        {getFieldError(errors, "host") && (
          <p className="text-xs text-destructive">
            {getFieldError(errors, "host")}
          </p>
        )}
      </div>
      <div className="space-y-2">
        <Label htmlFor="dnsRecordType">Record type</Label>
        <Select
          value={state.recordType}
          onValueChange={(recordType) => onChange({ ...state, recordType })}
        >
          <SelectTrigger id="dnsRecordType" data-testid="check-dns-record-type-select">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {["A", "AAAA", "CNAME", "MX", "NS", "TXT"].map((rt) => (
              <SelectItem key={rt} value={rt}>
                {rt}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <div className="space-y-2">
        <Label htmlFor="dnsNameserver">DNS server (optional)</Label>
        <Input
          id="dnsNameserver"
          type="text"
          placeholder="8.8.8.8:53 — defaults to system resolver"
          value={state.nameserver}
          onChange={(e) => onChange({ ...state, nameserver: e.target.value })}
          className={cn(
            getFieldError(errors, "nameserver") && "border-destructive",
          )}
          data-testid="check-dns-nameserver-input"
        />
        <p className="text-xs text-muted-foreground">
          Resolver to query, in host:port form. Leave blank to use the system
          resolver.
        </p>
        {getFieldError(errors, "nameserver") && (
          <p className="text-xs text-destructive">
            {getFieldError(errors, "nameserver")}
          </p>
        )}
      </div>
      {ipMode ? (
        <div className="space-y-2">
          <Label htmlFor="dnsExpectedIps">
            {t("form.dnsExpectedIps", "Expected IPs (optional)")}
          </Label>
          <TokenChipsInput
            id="dnsExpectedIps"
            value={state.expectedIps}
            onChange={(expectedIps) => onChange({ ...state, expectedIps })}
            validate={validateIp}
            normalize={(token) => token.trim().toLowerCase()}
            placeholder={state.recordType === "AAAA" ? "2606:4700::1111" : "1.1.1.1"}
            data-testid="check-dns-expected-ips"
            invalidTitle={t(
              "form.dnsExpectedIpInvalid",
              "Not a valid {{recordType}} address",
              { recordType: state.recordType === "AAAA" ? "IPv6" : "IPv4" },
            )}
            getRemoveLabel={(ip) =>
              t("form.dnsRemoveExpectedIp", "Remove {{ip}}", { ip })
            }
          />
          <p className="text-xs text-muted-foreground">
            {t(
              "form.dnsExpectedIpsHint",
              "The check fails unless every listed IP is present in the resolved answers. Leave empty to only require a successful resolution.",
            )}
          </p>
          {getFieldError(errors, "expected_ips") && (
            <p
              className="text-xs text-destructive"
              data-testid="check-dns-expected-ips-error"
            >
              {getFieldError(errors, "expected_ips")}
            </p>
          )}
        </div>
      ) : (
        <div className="space-y-2">
          <Label htmlFor="dnsExpectedValues">
            {t("form.dnsExpectedValues", "Expected values (optional)")}
          </Label>
          <Textarea
            id="dnsExpectedValues"
            rows={3}
            placeholder={
              state.recordType === "MX"
                ? "10 mail.example.com"
                : "target.example.com"
            }
            value={state.expectedValues}
            onChange={(e) =>
              onChange({ ...state, expectedValues: e.target.value })
            }
            data-testid="check-dns-expected-values"
          />
          <p className="text-xs text-muted-foreground">
            {t(
              "form.dnsExpectedValuesHint",
              "One value per line, matched exactly against the resolved records (MX as \"priority host\", names without a trailing dot). The check fails unless every listed value is present. Leave empty to only require a successful resolution.",
            )}
          </p>
          {getFieldError(errors, "expected_values") && (
            <p className="text-xs text-destructive">
              {getFieldError(errors, "expected_values")}
            </p>
          )}
        </div>
      )}
    </>
  );
}

// ── Domain (expiry) ──
export interface DomainState {
  domain: string;
  // Lookup method: "" (or "auto", default) tries RDAP first and falls back
  // to WHOIS on any RDAP failure; "rdap"/"whois" force one path. Empty
  // serializes to nothing, so existing checks upgrade transparently.
  method: string;
}

export const domainModule: CheckTypeModule<DomainState> = {
  types: ["domain"],
  fromConfig: (config) => ({
    domain: getConfigField(config, "domain"),
    method: getConfigField(config, "method"),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.domain) cfg.domain = state.domain;
    if (state.method && state.method !== "auto") cfg.method = state.method;
    const errors: FieldErrors = state.domain
      ? []
      : [{ name: "domain", message: "Domain is required" }];
    return { config: cfg, errors };
  },
  Fields: DomainFields,
};

function DomainFields({
  state,
  onChange,
  errors,
}: CheckTypeFieldsProps<DomainState>) {
  return (
    <div className="space-y-2">
      <Label htmlFor="domain">Domain</Label>
      <Input
        id="domain"
        type="text"
        placeholder="example.com"
        value={state.domain}
        onChange={(e) => onChange({ ...state, domain: e.target.value })}
        className={cn(getFieldError(errors, "domain") && "border-destructive")}
        data-testid="check-domain-input"
      />
      {getFieldError(errors, "domain") && (
        <p className="text-xs text-destructive">
          {getFieldError(errors, "domain")}
        </p>
      )}
    </div>
  );
}

// DomainAdvancedFields renders the "Advanced" section's lookup-method select
// (RDAP first with WHOIS fallback by default, or force one path).
export function DomainAdvancedFields({
  state,
  onChange,
}: CheckTypeFieldsProps<DomainState>) {
  return (
    <div className="space-y-2">
      <Label htmlFor="domainMethod">Lookup method</Label>
      <Select
        value={state.method || "auto"}
        onValueChange={(method) => onChange({ ...state, method })}
      >
        <SelectTrigger id="domainMethod" data-testid="check-domain-method-select">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="auto">Auto (RDAP, WHOIS fallback)</SelectItem>
          <SelectItem value="rdap">RDAP only</SelectItem>
          <SelectItem value="whois">WHOIS only</SelectItem>
        </SelectContent>
      </Select>
      <p className="text-xs text-muted-foreground">
        Auto tries RDAP first and falls back to WHOIS on any failure. RDAP
        only and WHOIS only never fall back.
      </p>
    </div>
  );
}

// domainAdvancedSummary drives the "Advanced" section's summary line for the
// lookup-method select above.
export function domainAdvancedSummary(state: DomainState): {
  text: string;
  customized: boolean;
} {
  const customized = !!state.method && state.method !== "auto";
  return { text: customized ? `method ${state.method}` : "", customized };
}

// ── DNSBL ──
export interface DnsblState {
  target: string;
  blocklists: string;
  nameserver: string;
}

export const dnsblModule: CheckTypeModule<DnsblState> = {
  types: ["dnsbl"],
  fromConfig: (config) => ({
    target: getConfigField(config, "target"),
    blocklists: Array.isArray(config.blocklists)
      ? (config.blocklists as string[]).join("\n")
      : getConfigField(config, "blocklists"),
    nameserver: getConfigField(config, "nameserver"),
  }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.target) cfg.target = state.target;
    const zones = splitBlocklists(state.blocklists);
    if (zones.length > 0) cfg.blocklists = zones;
    if (state.nameserver) cfg.nameserver = state.nameserver;
    const errors: FieldErrors = state.target
      ? []
      : [{ name: "target", message: "Target IP or hostname is required" }];
    return { config: cfg, errors };
  },
  Fields: DnsblFields,
};

function DnsblFields({
  state,
  onChange,
  errors,
}: CheckTypeFieldsProps<DnsblState>) {
  const { t } = useTranslation("checks");
  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="dnsblTarget">{t("dnsbl.target")}</Label>
        <Input
          id="dnsblTarget"
          type="text"
          placeholder="203.0.113.10"
          value={state.target}
          onChange={(e) => onChange({ ...state, target: e.target.value })}
          className={cn(getFieldError(errors, "target") && "border-destructive")}
          data-testid="check-dnsbl-target-input"
        />
        <p className="text-xs text-muted-foreground">{t("dnsbl.targetHelp")}</p>
        {getFieldError(errors, "target") && (
          <p className="text-xs text-destructive">
            {getFieldError(errors, "target")}
          </p>
        )}
      </div>
      <div className="space-y-2">
        <Label htmlFor="dnsblBlocklists">{t("dnsbl.blocklists")}</Label>
        <Textarea
          id="dnsblBlocklists"
          rows={4}
          placeholder={"zen.spamhaus.org\nbl.spamcop.net"}
          value={state.blocklists}
          onChange={(e) => onChange({ ...state, blocklists: e.target.value })}
          data-testid="check-dnsbl-blocklists-input"
        />
        <p className="text-xs text-muted-foreground">
          {t("dnsbl.blocklistsHelp")}
        </p>
      </div>
      <div className="space-y-2">
        <Label htmlFor="dnsblNameserver">{t("dnsbl.nameserver")}</Label>
        <Input
          id="dnsblNameserver"
          type="text"
          placeholder="127.0.0.1:53"
          value={state.nameserver}
          onChange={(e) => onChange({ ...state, nameserver: e.target.value })}
          className={cn(
            getFieldError(errors, "nameserver") && "border-destructive",
          )}
          data-testid="check-dnsbl-nameserver-input"
        />
        <p className="text-xs text-muted-foreground">
          {t("dnsbl.nameserverHelp")}
        </p>
        {getFieldError(errors, "nameserver") && (
          <p className="text-xs text-destructive">
            {getFieldError(errors, "nameserver")}
          </p>
        )}
      </div>
    </>
  );
}
