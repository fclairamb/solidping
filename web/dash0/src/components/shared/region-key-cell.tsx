import { useState } from "react";
import { useTranslation } from "react-i18next";
import { AlertTriangle, Check as CheckIcon, ClipboardPaste, Copy, Download, KeyRound, RotateCw } from "lucide-react";

import { useDeleteRegionKey, useSetRegionKey, type PrivateRegion } from "@/api/hooks";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { RewrapButton } from "@/components/shared/rewrap-button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { canSealInBrowser, generateRegionKey, isAgeRecipient, type GeneratedRegionKey } from "@/lib/sealing";

/** The offline region key command (a verbatim command). */
const REGION_KEY_COMMAND = ["solidping", "agent", "region-key", "generate"].join(" ");

/** The agent environment line carrying a region key (a verbatim command). */
function agentEnvLine(identity: string): string {
  return ["SP_AGENT_REGION_KEYS", identity].join("=");
}

/**
 * The sealing key mode of a private region (spec 2026-11-08-01 §2): a key per
 * agent (default) or one shared region key. The shared key is generated in
 * the browser, shown once, and only its public recipient reaches the server.
 * The paranoid path pastes the public key of an identity generated offline
 * (`solidping agent region-key generate`, spec 2026-11-10-03 §3).
 */
export function RegionKeyCell({ org, region }: { org: string; region: PrivateRegion }) {
  const { t } = useTranslation(["org"]);
  const setKey = useSetRegionKey(org);
  const deleteKey = useDeleteRegionKey(org);
  const [generated, setGenerated] = useState<GeneratedRegionKey | null>(null);
  const [copied, setCopied] = useState(false);
  const [pasting, setPasting] = useState(false);
  // Kept once started, so its result stays on screen after the count drops.
  const [rewrapStarted, setRewrapStarted] = useState(false);
  const [pasted, setPasted] = useState("");
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

  // Picking `shared` generates the region key (nothing changes until it is
  // confirmed); picking `perAgent` drops it.
  const changeMode = (next: string) => {
    if (next === "shared" && !shared) void generate();
    if (next === "perAgent" && shared) deleteKey.mutate(region.slug);
  };

  const confirm = async () => {
    if (!generated) return;
    await setKey.mutateAsync({ slug: region.slug, recipient: generated.recipient });
    setGenerated(null);
  };

  const confirmPasted = async () => {
    if (!isAgeRecipient(pasted)) return;
    await setKey.mutateAsync({ slug: region.slug, recipient: pasted.trim() });
    setPasting(false);
    setPasted("");
  };

  return (
    <div className="space-y-1" data-testid={`region-key-${region.slug}`}>
      <div className="flex flex-wrap items-center gap-1">
        <div data-testid={`region-key-mode-${region.slug}`}>
          <Select
            value={shared ? "shared" : "perAgent"}
            onValueChange={changeMode}
            disabled={setKey.isPending || deleteKey.isPending}
          >
            <SelectTrigger
              className="h-8 w-auto min-w-40"
              aria-label={t("privateLocations.regionKey.modeLabel")}
              data-testid="region-key-mode-select"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="perAgent" data-testid="region-key-mode-per-agent">
                {t("privateLocations.regionKey.perAgent")}
              </SelectItem>
              <SelectItem value="shared" disabled={!canSealInBrowser()} data-testid="region-key-mode-shared">
                {t("privateLocations.regionKey.shared")}
              </SelectItem>
            </SelectContent>
          </Select>
        </div>
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
      {shared && ((region.checksNeedingReseal ?? 0) > 0 || rewrapStarted) && (
        <RewrapButton
          org={org}
          region={region.region}
          testId={region.slug}
          onStarted={() => setRewrapStarted(true)}
        />
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
        <Button
          variant="outline"
          size="sm"
          onClick={() => setPasting(true)}
          disabled={setKey.isPending}
          data-testid="region-key-paste-button"
        >
          <ClipboardPaste className="h-4 w-4" />
          {t("privateLocations.regionKey.paste")}
        </Button>
      </div>
      <Dialog open={pasting} onOpenChange={(open) => !open && setPasting(false)}>
        <DialogContent data-testid="region-key-paste-dialog">
          <DialogHeader>
            <DialogTitle>{t("privateLocations.regionKey.pasteTitle", { region: region.region })}</DialogTitle>
            <DialogDescription>{t("privateLocations.regionKey.pasteDescription")}</DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <pre className="max-w-full overflow-x-auto rounded bg-muted p-2 text-xs">
              {REGION_KEY_COMMAND}
            </pre>
            <Input
              value={pasted}
              onChange={(event) => setPasted(event.target.value)}
              placeholder="age1…"
              aria-label={t("privateLocations.regionKey.pasteLabel")}
              data-testid="region-key-paste-input"
            />
            {pasted.trim() !== "" && !isAgeRecipient(pasted) && (
              <p className="text-xs text-status-error" data-testid="region-key-paste-invalid">
                {t("privateLocations.regionKey.pasteInvalid")}
              </p>
            )}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setPasting(false)} data-testid="region-key-paste-cancel">
              {t("privateLocations.regionKey.cancel")}
            </Button>
            <Button
              onClick={confirmPasted}
              disabled={!isAgeRecipient(pasted) || setKey.isPending}
              data-testid="region-key-paste-confirm"
            >
              {t("privateLocations.regionKey.pasteConfirm")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
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
