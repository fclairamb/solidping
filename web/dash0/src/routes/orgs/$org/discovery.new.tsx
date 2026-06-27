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
import { Alert, AlertDescription } from "@/components/ui/alert";
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
  useDiscoveryTypes,
  useIntegrations,
  useStartDiscoveryScan,
  type StartDiscoveryScanRequest,
} from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/discovery/new")({
  component: NewScanPage,
});

function NewScanPage() {
  const { t } = useTranslation("discovery");
  const { org } = Route.useParams();
  const navigate = useNavigate();
  const startScan = useStartDiscoveryScan(org);
  const { data: types } = useDiscoveryTypes(org);
  const { data: channels } = useIntegrations(org);

  // Granted Freebox source channels — Freebox is only offered when one exists.
  const grantedFreeboxChannels = useMemo(
    () =>
      (channels ?? []).filter(
        (c) =>
          canSource(c.type) &&
          (c.settings?.status as string | undefined) === "granted",
      ),
    [channels],
  );

  // Kubernetes cluster connections — Kubernetes is only offered when one exists.
  const kubernetesClusters = useMemo(
    () => (channels ?? []).filter((c) => c.type === "kubernetes"),
    [channels],
  );

  // The selectable types are the registry's, gated by client capability: Freebox
  // only appears when a granted channel exists; Kubernetes only when a cluster
  // connection exists.
  const availableTypes = useMemo(() => {
    const registered = (types ?? []).map((d) => d.type);
    // Default to LAN even before the registry list loads, so the form is usable.
    const set = registered.length > 0 ? registered : ["lan"];
    return set.filter((typ) => {
      if (typ === "freebox") return grantedFreeboxChannels.length > 0;
      if (typ === "kubernetes") return kubernetesClusters.length > 0;
      return true;
    });
  }, [types, grantedFreeboxChannels, kubernetesClusters]);

  const [type, setType] = useState("lan");
  const [selectedChannelUid, setSelectedChannelUid] = useState("");
  const [selectedClusterUid, setSelectedClusterUid] = useState("");
  const [namespacesText, setNamespacesText] = useState("");
  const [cidrsText, setCidrsText] = useState("");
  const [containerHostsText, setContainerHostsText] = useState(
    "unix:///var/run/docker.sock",
  );
  const [ports, setPorts] = useState("");
  const [timeout, setTimeout] = useState("");
  const [concurrency, setConcurrency] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [advanced, setAdvanced] = useState(false);

  const isPending = startScan.isPending;

  // Estimate the total host count and chunk count for the entered CIDRs.
  const cidrEstimate = useMemo(() => {
    const tokens = cidrsText.split(/[\n,]+/).map((s) => s.trim()).filter(Boolean);
    let totalHosts = 0;
    for (const token of tokens) {
      const match = /^(\d{1,3}\.){3}\d{1,3}\/(\d{1,2})$/.exec(token);
      if (!match) continue;
      const prefix = parseInt(match[2], 10);
      if (prefix < 0 || prefix > 32) continue;
      totalHosts += Math.pow(2, 32 - prefix);
    }
    const estimatedChunks = Math.ceil(totalHosts / 4096);
    return { totalHosts, estimatedChunks };
  }, [cidrsText]);

  // requiredFieldMissing flags the per-type required input being empty.
  const requiredFieldMissing =
    type === "lan"
      ? !cidrsText.trim()
      : type === "container"
        ? !containerHostsText.trim()
        : type === "kubernetes"
          ? !selectedClusterUid
          : !selectedChannelUid;

  const submitDisabled = isPending || !confirmed || requiredFieldMissing;

  // typeLabel maps a registry type id to its i18n label, falling back to the id.
  const typeLabel = (typ: string) => t(`method.${typ}`, typ);

  const buildRequest = (): StartDiscoveryScanRequest | null => {
    if (type === "freebox") {
      if (!selectedChannelUid) return null;
      return { type: "freebox", parameters: { channelUid: selectedChannelUid } };
    }

    if (type === "container") {
      const hosts = containerHostsText
        .split(/[\n,]+/)
        .map((h) => h.trim())
        .filter(Boolean);
      if (hosts.length === 0) return null;
      return { type: "container", parameters: { hosts } };
    }

    if (type === "kubernetes") {
      if (!selectedClusterUid) return null;
      const namespaces = namespacesText
        .split(/[\n,]+/)
        .map((n) => n.trim())
        .filter(Boolean);
      const parameters: Record<string, unknown> = {
        clusterUid: selectedClusterUid,
      };
      if (namespaces.length > 0) parameters.namespaces = namespaces;
      return { type: "kubernetes", parameters };
    }

    // LAN
    const cidrs = cidrsText
      .split(/[\n,]+/)
      .map((c) => c.trim())
      .filter(Boolean);
    if (cidrs.length === 0) return null;

    const parameters: Record<string, unknown> = { cidrs };
    if (ports.trim()) {
      parameters.ports = ports
        .split(",")
        .map((p) => parseInt(p.trim(), 10))
        .filter((p) => !isNaN(p));
    }
    if (timeout.trim()) parameters.timeout = timeout.trim();
    if (concurrency.trim()) parameters.concurrency = parseInt(concurrency.trim(), 10);

    return { type: "lan", parameters };
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!confirmed) return;

    const req = buildRequest();
    if (!req) {
      toast.error(t("cidrsLabel") + " required");
      return;
    }

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
            <Select value={type} onValueChange={setType}>
              <SelectTrigger id="scan-method" aria-label={t("scanMethod")}>
                <SelectValue placeholder={t("scanMethod")} />
              </SelectTrigger>
              <SelectContent>
                {availableTypes.map((typ) => (
                  <SelectItem key={typ} value={typ}>
                    {typeLabel(typ)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          {type === "lan" ? (
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
          ) : type === "container" ? (
            <div className="space-y-1.5">
              <Label htmlFor="container-hosts">{t("containerHosts")}</Label>
              <Textarea
                id="container-hosts"
                value={containerHostsText}
                onChange={(e) => setContainerHostsText(e.target.value)}
                rows={3}
                required
              />
              <p className="text-xs text-muted-foreground">
                {t("containerHostsHelp")}
              </p>
            </div>
          ) : type === "freebox" ? (
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
          ) : type === "kubernetes" ? (
            <>
              <div className="space-y-1.5">
                <Label htmlFor="k8s-cluster">{t("selectCluster")}</Label>
                <Select
                  value={selectedClusterUid}
                  onValueChange={setSelectedClusterUid}
                >
                  <SelectTrigger
                    id="k8s-cluster"
                    aria-label={t("selectCluster")}
                  >
                    <SelectValue placeholder={t("selectCluster")} />
                  </SelectTrigger>
                  <SelectContent>
                    {kubernetesClusters.map((c) => (
                      <SelectItem key={c.uid} value={c.uid}>
                        {c.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="k8s-namespaces">{t("clusterNamespaces")}</Label>
                <Input
                  id="k8s-namespaces"
                  placeholder="prod, staging"
                  value={namespacesText}
                  onChange={(e) => setNamespacesText(e.target.value)}
                />
                <p className="text-xs text-muted-foreground">
                  {t("clusterNamespacesHelp")}
                </p>
              </div>
            </>
          ) : null}

          {type === "lan" && cidrEstimate.totalHosts > 4096 && (
            <Alert variant="warning" data-testid="large-range-warning">
              <AlertDescription>
                {t("largeRangeWarning", {
                  hosts: cidrEstimate.totalHosts.toLocaleString(),
                  chunks: cidrEstimate.estimatedChunks.toLocaleString(),
                })}
              </AlertDescription>
            </Alert>
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
