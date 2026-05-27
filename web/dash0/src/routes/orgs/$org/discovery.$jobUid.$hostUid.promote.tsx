import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, useNavigate, Link } from "@tanstack/react-router";
import { ArrowLeft, CirclePlus, Loader2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Input } from "@/components/ui/input";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  useListCandidateHosts,
  usePromoteCandidate,
  type SuggestedCheck,
  type PromoteCheckSpec,
} from "@/api/hooks";

export const Route = createFileRoute(
  "/orgs/$org/discovery/$jobUid/$hostUid/promote",
)({
  component: PromoteHostPage,
});

const CHECK_TYPES = ["http", "tcp", "ping", "ssl", "dns"] as const;

// configHint summarizes a suggested check's config into a short string
// (url / host:port / host) for display next to the checkbox.
function configHint(suggested: SuggestedCheck): string {
  const cfg = suggested.config ?? {};
  if (typeof cfg.url === "string") return cfg.url;
  if (cfg.host !== undefined && cfg.port !== undefined) {
    return `${String(cfg.host)}:${String(cfg.port)}`;
  }
  if (cfg.host !== undefined) return String(cfg.host);
  return "";
}

// periodToIso8601 converts a human-friendly period ("1m", "30s", "1h30m") into
// the ISO 8601 duration the API expects ("PT1M"). Already-valid ISO 8601 / HMS
// strings pass through. Returns undefined when nothing parseable was entered, so
// the caller can omit the field and let the server apply its default.
function periodToIso8601(input: string): string | undefined {
  const trimmed = input.trim();
  if (!trimmed) return undefined;
  if (/^PT/i.test(trimmed) || /^\d{1,3}:\d{2}:\d{2}$/.test(trimmed)) {
    return trimmed;
  }
  let totalSeconds = 0;
  let matched = false;
  for (const m of trimmed.matchAll(/(\d+)\s*([hms])/gi)) {
    matched = true;
    const n = Number(m[1]);
    const unit = m[2].toLowerCase();
    totalSeconds += unit === "h" ? n * 3600 : unit === "m" ? n * 60 : n;
  }
  if (!matched && /^\d+$/.test(trimmed)) {
    totalSeconds = Number(trimmed);
    matched = true;
  }
  if (!matched || totalSeconds <= 0) return undefined;
  const h = Math.floor(totalSeconds / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  const s = totalSeconds % 60;
  return `PT${h ? `${h}H` : ""}${m ? `${m}M` : ""}${s ? `${s}S` : ""}`;
}

function PromoteHostPage() {
  const { t } = useTranslation("discovery");
  const { org, jobUid, hostUid } = Route.useParams();
  const navigate = useNavigate();
  const promote = usePromoteCandidate(org);

  // Load the host list and find the matching host.
  const { data: hosts } = useListCandidateHosts(org, { jobUid });
  const host = hosts?.find((h) => h.uid === hostUid);

  const suggested = useMemo<SuggestedCheck[]>(
    () => host?.suggestedChecks ?? [],
    [host],
  );
  const hasSuggestions = suggested.length > 0;

  const baseName = host ? host.hostname || host.ip : "";
  const [name, setName] = useState("");
  const [period, setPeriod] = useState("1m");
  // Which suggested check indices are ticked. Pre-checked once the host loads.
  const [selected, setSelected] = useState<Record<number, boolean>>({});
  // Manual fallback type (used when there are no suggested checks).
  const [manualType, setManualType] = useState("tcp");

  // Initialize defaults once the host loads.
  const [initialized, setInitialized] = useState(false);
  if (host && !initialized) {
    setName(host.hostname || host.ip);
    setManualType(suggested[0]?.type ?? "tcp");
    const initSel: Record<number, boolean> = {};
    suggested.forEach((_, i) => {
      initSel[i] = true;
    });
    setSelected(initSel);
    setInitialized(true);
  }

  const selectedSuggestions = useMemo(
    () => suggested.filter((_, i) => selected[i]),
    [suggested, selected],
  );

  const canSubmit = hasSuggestions ? selectedSuggestions.length > 0 : !!name;

  const toggle = (index: number) => {
    setSelected((prev) => ({ ...prev, [index]: !prev[index] }));
  };

  const buildChecks = (): PromoteCheckSpec[] => {
    const types = hasSuggestions
      ? selectedSuggestions.map((s) => s.type)
      : [manualType];
    const multi = types.length > 1;

    return types.map((type) => ({
      checkType: type,
      name: multi ? `${name} (${type})` : name,
      period: periodToIso8601(period),
    }));
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();

    try {
      await promote.mutateAsync({
        uid: hostUid,
        req: { checks: buildChecks() },
      });
      toast.success(t("promoted_success"));
      navigate({
        to: "/orgs/$org/discovery/$jobUid",
        params: { org, jobUid },
      });
    } catch {
      toast.error(t("promoted_failed"));
    }
  };

  return (
    <Card className="max-w-xl">
      <CardHeader>
        <div className="flex items-center gap-3">
          <Button asChild variant="ghost" size="icon">
            <Link to="/orgs/$org/discovery/$jobUid" params={{ org, jobUid }}>
              <ArrowLeft className="h-4 w-4" />
            </Link>
          </Button>
          <div>
            <CardTitle>{t("promoteTitle")}</CardTitle>
            {host && (
              <CardDescription className="font-mono text-xs">
                {host.ip}
                {host.hostname ? ` (${host.hostname})` : ""}
              </CardDescription>
            )}
          </div>
        </div>
      </CardHeader>
      <CardContent>
        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-1.5">
            <Label htmlFor="name">{t("name")}</Label>
            <Input
              id="name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={baseName}
              required
            />
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="period">{t("period")}</Label>
            <Input
              id="period"
              value={period}
              onChange={(e) => setPeriod(e.target.value)}
              placeholder="1m"
            />
          </div>

          {hasSuggestions ? (
            <div className="space-y-2">
              <Label>{t("selectChecks")}</Label>
              <div className="space-y-2 rounded-md border p-3">
                {suggested.map((s, i) => {
                  const hint = configHint(s);
                  return (
                    <label
                      key={`${s.type}-${i}`}
                      className="flex items-center gap-3 text-sm"
                    >
                      <Checkbox
                        checked={!!selected[i]}
                        onCheckedChange={() => toggle(i)}
                        aria-label={s.type}
                      />
                      <span className="font-medium">{s.type}</span>
                      {hint && (
                        <span className="font-mono text-xs text-muted-foreground">
                          {hint}
                        </span>
                      )}
                    </label>
                  );
                })}
              </div>
            </div>
          ) : (
            <div className="space-y-1.5">
              <Label htmlFor="checkType">{t("noSuggestedChecks")}</Label>
              <Select value={manualType} onValueChange={setManualType}>
                <SelectTrigger id="checkType">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {CHECK_TYPES.map((type) => (
                    <SelectItem key={type} value={type}>
                      {type}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}

          <div className="flex justify-end gap-2">
            <Button asChild type="button" variant="outline">
              <Link to="/orgs/$org/discovery/$jobUid" params={{ org, jobUid }}>
                {t("cancel")}
              </Link>
            </Button>
            <Button type="submit" disabled={promote.isPending || !canSubmit}>
              {promote.isPending ? (
                <Loader2 className="h-4 w-4 mr-1 animate-spin" />
              ) : (
                <CirclePlus className="h-4 w-4 mr-1" />
              )}
              {t("createChecks")}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}
