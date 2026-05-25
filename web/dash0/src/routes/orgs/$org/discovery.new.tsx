import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, useNavigate, Link } from "@tanstack/react-router";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
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
  canSource,
  useChannels,
  useStartDiscoveryScan,
  useStartFreeboxScan,
} from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/discovery/new")({
  component: NewScanPage,
});

type ScanMethod = "lan" | "freebox";

function NewScanPage() {
  const { t } = useTranslation("discovery");
  const { org } = Route.useParams();
  const navigate = useNavigate();
  const startScan = useStartDiscoveryScan(org);
  const startFreeboxScan = useStartFreeboxScan(org);
  const { data: channels } = useChannels(org);

  // Source integrations (canSource) that can drive a discovery scan, gated on the
  // Freebox `granted` pairing state — mirrors the filter in `check-form.tsx`.
  const grantedFreeboxChannels = useMemo(
    () =>
      (channels ?? []).filter(
        (c) =>
          canSource(c.type) &&
          (c.settings?.status as string | undefined) === "granted",
      ),
    [channels],
  );

  const [method, setMethod] = useState<ScanMethod>("lan");
  const [selectedChannelUid, setSelectedChannelUid] = useState("");
  const [cidrsText, setCidrsText] = useState("");
  const [ports, setPorts] = useState("");
  const [timeout, setTimeout] = useState("");
  const [concurrency, setConcurrency] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [advanced, setAdvanced] = useState(false);

  const isPending = startScan.isPending || startFreeboxScan.isPending;

  const submitDisabled =
    isPending ||
    !confirmed ||
    (method === "lan" ? !cidrsText.trim() : !selectedChannelUid);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!confirmed) return;

    if (method === "freebox") {
      if (!selectedChannelUid) return;
      try {
        const result = await startFreeboxScan.mutateAsync(selectedChannelUid);
        const jobUid = result?.data?.uid;
        toast.success(t("freeboxScanStarted"));
        navigate({
          to: "/orgs/$org/discovery/$jobUid",
          params: { org, jobUid: jobUid ?? "" },
        });
      } catch {
        toast.error(t("freeboxScanFailed"));
      }
      return;
    }

    const cidrs = cidrsText
      .split(/[\n,]+/)
      .map((c) => c.trim())
      .filter(Boolean);

    if (cidrs.length === 0) {
      toast.error(t("cidrsLabel") + " required");
      return;
    }

    const req: Parameters<typeof startScan.mutateAsync>[0] = { cidrs };

    if (ports.trim()) {
      req.ports = ports
        .split(",")
        .map((p) => parseInt(p.trim(), 10))
        .filter((p) => !isNaN(p));
    }

    if (timeout.trim()) req.timeout = timeout.trim();
    if (concurrency.trim()) req.concurrency = parseInt(concurrency.trim(), 10);

    try {
      const result = await startScan.mutateAsync(req);
      const jobUid = result?.data?.uid;
      toast.success(t("scanStarted"));
      navigate({
        to: "/orgs/$org/discovery/$jobUid",
        params: { org, jobUid: jobUid ?? "" },
      });
    } catch {
      toast.error(t("scanFailed"));
    }
  };

  return (
    <Card className="max-w-xl">
      <CardHeader>
        <div className="flex items-center justify-between gap-4">
          <CardTitle>{t("newScanTitle")}</CardTitle>
          <Button asChild variant="ghost" size="sm">
            <Link to="/orgs/$org/discovery" params={{ org }}>
              {t("cancel")}
            </Link>
          </Button>
        </div>
        <CardDescription className="text-xs text-amber-600">
          {t("workerNote")}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={handleSubmit} className="space-y-5">
          <div className="space-y-1.5">
            <Label htmlFor="scan-method">{t("scanMethod")}</Label>
            <Select
              value={method}
              onValueChange={(v) => setMethod(v as ScanMethod)}
            >
              <SelectTrigger id="scan-method" aria-label={t("scanMethod")}>
                <SelectValue placeholder={t("scanMethod")} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="lan">{t("methodLan")}</SelectItem>
                {grantedFreeboxChannels.length > 0 && (
                  <SelectItem value="freebox">{t("methodFreebox")}</SelectItem>
                )}
              </SelectContent>
            </Select>
          </div>

          {method === "lan" ? (
            <>
              <div className="space-y-1.5">
                <Label htmlFor="cidrs">{t("cidrsLabel")}</Label>
                <Textarea
                  id="cidrs"
                  placeholder={t("cidrsPlaceholder")}
                  value={cidrsText}
                  onChange={(e) => setCidrsText(e.target.value)}
                  rows={4}
                  required
                />
                <p className="text-xs text-muted-foreground">{t("cidrsHelp")}</p>
              </div>

              <div>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() => setAdvanced(!advanced)}
                >
                  {t("advanced")} {advanced ? "▲" : "▼"}
                </Button>
                {advanced && (
                  <div className="space-y-4 pt-2">
                    <div className="space-y-1.5">
                      <Label htmlFor="ports">{t("portsLabel")}</Label>
                      <Input
                        id="ports"
                        placeholder="22, 80, 443"
                        value={ports}
                        onChange={(e) => setPorts(e.target.value)}
                      />
                      <p className="text-xs text-muted-foreground">{t("portsHelp")}</p>
                    </div>
                    <div className="space-y-1.5">
                      <Label htmlFor="timeout">{t("timeoutLabel")}</Label>
                      <Input
                        id="timeout"
                        placeholder="1s"
                        value={timeout}
                        onChange={(e) => setTimeout(e.target.value)}
                      />
                      <p className="text-xs text-muted-foreground">{t("timeoutHelp")}</p>
                    </div>
                    <div className="space-y-1.5">
                      <Label htmlFor="concurrency">{t("concurrencyLabel")}</Label>
                      <Input
                        id="concurrency"
                        type="number"
                        min={1}
                        max={256}
                        placeholder="64"
                        value={concurrency}
                        onChange={(e) => setConcurrency(e.target.value)}
                      />
                      <p className="text-xs text-muted-foreground">{t("concurrencyHelp")}</p>
                    </div>
                  </div>
                )}
              </div>
            </>
          ) : (
            <div className="space-y-1.5">
              <Label htmlFor="freebox-channel">{t("selectFreeboxChannel")}</Label>
              <Select
                value={selectedChannelUid}
                onValueChange={setSelectedChannelUid}
              >
                <SelectTrigger
                  id="freebox-channel"
                  aria-label={t("selectFreeboxChannel")}
                >
                  <SelectValue placeholder={t("selectFreeboxChannel")} />
                </SelectTrigger>
                <SelectContent>
                  {grantedFreeboxChannels.map((c) => (
                    <SelectItem key={c.uid} value={c.uid}>
                      {c.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          )}

          <div className="flex items-start gap-2">
            <Checkbox
              id="confirm"
              checked={confirmed}
              onCheckedChange={(v) => setConfirmed(!!v)}
            />
            <Label htmlFor="confirm" className="text-sm font-normal cursor-pointer">
              {t("confirmation")}
            </Label>
          </div>

          <div className="flex justify-end">
            <Button type="submit" disabled={submitDisabled}>
              {isPending ? (
                <>
                  <Loader2 className="h-4 w-4 mr-1 animate-spin" />
                  {t("startScan")}…
                </>
              ) : (
                t("startScan")
              )}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}
