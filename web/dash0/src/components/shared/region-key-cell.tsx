import { useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle, Check as CheckIcon, Copy, Download, KeyRound, RotateCw, Trash2 } from "lucide-react";

import { useDeleteRegionKey, useSetRegionKey, type PrivateRegion } from "@/api/hooks";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { canSealInBrowser, generateRegionKey, type GeneratedRegionKey } from "@/lib/sealing";

/** The agent environment line carrying a region key (a verbatim command). */
function agentEnvLine(identity: string): string {
  return ["SP_AGENT_REGION_KEYS", identity].join("=");
}

/**
 * The sealing key mode of a private region (spec 2026-11-08-01 §2): a key per
 * agent (default) or one shared region key. The shared key is generated in
 * the browser, shown once, and only its public recipient reaches the server.
 */
export function RegionKeyCell({ org, region }: { org: string; region: PrivateRegion }) {
  const { t } = useTranslation(["org"]);
  const setKey = useSetRegionKey(org);
  const deleteKey = useDeleteRegionKey(org);
  const [generated, setGenerated] = useState<GeneratedRegionKey | null>(null);
  const [copied, setCopied] = useState(false);
  const shared = region.keyMode === "shared" && region.regionKey;

  const generate = async () => setGenerated(await generateRegionKey());

  const copy = async () => {
    if (!generated) return;
    await navigator.clipboard.writeText(generated.identity);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };

  const download = () => {
    if (!generated) return;
    const body = `# SolidPing region key for ${region.region}\n# public key: ${generated.recipient}\n${generated.identity}\n`;
    const url = URL.createObjectURL(new Blob([body], { type: "text/plain" }));
    const link = document.createElement("a");
    link.href = url;
    link.download = `region-key-${region.slug}.txt`;
    link.click();
    URL.revokeObjectURL(url);
  };

  const confirm = async () => {
    if (!generated) return;
    await setKey.mutateAsync({ slug: region.slug, recipient: generated.recipient });
    setGenerated(null);
  };

  return (
    <div className="space-y-1" data-testid={`region-key-${region.slug}`}>
      <div className="flex flex-wrap items-center gap-1">
        <Badge variant={shared ? "success" : "secondary"} data-testid={`region-key-mode-${region.slug}`}>
          {shared ? t("privateLocations.regionKey.shared") : t("privateLocations.regionKey.perAgent")}
        </Badge>
        {shared && (
          <code className="text-xs text-muted-foreground" data-testid={`region-key-fingerprint-${region.slug}`}>
            {region.regionKey?.fingerprint}
          </code>
        )}
      </div>
      {shared && (region.agentsWithRegionKey ?? 0) === 0 && (
        <p className="flex items-center gap-1 text-xs text-status-warning" data-testid={`region-key-no-agent-${region.slug}`}>
          <AlertTriangle className="h-3 w-3" />
          {t("privateLocations.regionKey.noAgent")}
        </p>
      )}
      {shared && (region.checksNeedingReseal ?? 0) > 0 && (
        <p className="text-xs text-muted-foreground" data-testid={`region-key-stale-${region.slug}`}>
          {t("privateLocations.regionKey.stale", { count: region.checksNeedingReseal })}
        </p>
      )}
      <div className="flex flex-wrap gap-1">
        <Button
          variant="outline"
          size="sm"
          onClick={generate}
          disabled={!canSealInBrowser() || setKey.isPending}
          data-testid={shared ? "region-key-rotate-button" : "region-key-generate-button"}
        >
          {shared ? <RotateCw className="h-4 w-4" /> : <KeyRound className="h-4 w-4" />}
          {shared ? t("privateLocations.regionKey.rotate") : t("privateLocations.regionKey.generate")}
        </Button>
        {shared && (
          <Button
            variant="ghost"
            size="sm"
            className="text-destructive"
            onClick={() => deleteKey.mutate(region.slug)}
            disabled={deleteKey.isPending}
            data-testid="region-key-remove-button"
          >
            <Trash2 className="h-4 w-4" />
            {t("privateLocations.regionKey.remove")}
          </Button>
        )}
      </div>
      <Dialog open={!!generated} onOpenChange={(open) => !open && setGenerated(null)}>
        <DialogContent data-testid="region-key-dialog">
          <DialogHeader>
            <DialogTitle>{t("privateLocations.regionKey.dialogTitle", { region: region.region })}</DialogTitle>
            <DialogDescription>{t("privateLocations.regionKey.dialogDescription")}</DialogDescription>
          </DialogHeader>
          {generated && (
            <div className="min-w-0 space-y-3">
              <div className="flex min-w-0 items-center gap-2">
                <code
                  className="min-w-0 flex-1 overflow-x-auto rounded bg-muted px-2 py-1.5 text-xs"
                  data-testid="region-key-secret"
                >
                  {generated.identity}
                </code>
                <Button variant="outline" size="icon" onClick={copy} data-testid="region-key-copy">
                  {copied ? <CheckIcon className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
                </Button>
                <Button variant="outline" size="icon" onClick={download} data-testid="region-key-download">
                  <Download className="h-4 w-4" />
                </Button>
              </div>
              <p className="text-xs text-muted-foreground">{t("privateLocations.regionKey.distribute")}</p>
              <pre className="max-w-full overflow-x-auto rounded bg-muted p-2 text-xs">
                {agentEnvLine(generated.identity)}
              </pre>
            </div>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => setGenerated(null)} data-testid="region-key-cancel">
              {t("privateLocations.regionKey.cancel")}
            </Button>
            <Button onClick={confirm} disabled={setKey.isPending} data-testid="region-key-confirm">
              {t("privateLocations.regionKey.confirm")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
