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
import type { CheckTypeModule } from "./index";
import type { CheckConfig, CheckTypeFieldsProps, FieldErrors } from "./common";
import { getConfigField, splitBlocklists } from "./common";

// ── DNS ──
// The queried domain is bound to the backend `host` key (label stays "Domain").
export interface DnsState {
  host: string;
  nameserver: string;
  recordType: string;
}

export const dnsModule: CheckTypeModule<DnsState> = {
  types: ["dns"],
  fromConfig: (config) => ({
    host: getConfigField(config, "host"),
    nameserver: getConfigField(config, "nameserver"),
    recordType: getConfigField(config, "record_type") || "A",
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
    return { config: cfg, errors };
  },
  Fields: DnsFields,
};

function DnsFields({ state, onChange, errors }: CheckTypeFieldsProps<DnsState>) {
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
    </>
  );
}

// ── Domain (expiry) ──
export interface DomainState {
  domain: string;
}

export const domainModule: CheckTypeModule<DomainState> = {
  types: ["domain"],
  fromConfig: (config) => ({ domain: getConfigField(config, "domain") }),
  toConfig: (state) => {
    const cfg: CheckConfig = {};
    if (state.domain) cfg.domain = state.domain;
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
