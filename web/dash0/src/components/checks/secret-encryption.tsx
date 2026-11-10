import { useTranslation } from "react-i18next";
import { Lock, Server } from "lucide-react";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import type { SealingRecipient } from "@/lib/sealing";

/** Who encrypts a check's secrets (spec 2026-11-08-01 §1). */
export type SecretEncryptionMode = "server" | "browser";

/** Why browser encryption is not offered for the current form state. */
export type BrowserSealingBlocker = "sharedRegion" | "insecureContext" | null;

interface SecretEncryptionPanelProps {
  mode: SecretEncryptionMode;
  onModeChange: (mode: SecretEncryptionMode) => void;
  /** The org requires browser sealing: the server choice is gone. */
  required: boolean;
  blocker: BrowserSealingBlocker;
  /** Recipients of the selected private regions (browser mode only). */
  recipients: SealingRecipient[];
  regions: string[];
  /** The stored secrets are browser-sealed (edit of a sealed check). */
  storedSealed: boolean;
  /** Shown once after the form switched back to server mode by itself. */
  switchedNotice: boolean;
  /** The stored blob is being replaced: every secret input is open. */
  replacing: boolean;
  onReplace: () => void;
}

/**
 * The secret encryption choice above a check's secret inputs, with the lock
 * badge of browser mode. A presentational component: the form owns the state
 * and seals on submit.
 */
export function SecretEncryptionPanel({
  mode,
  onModeChange,
  required,
  blocker,
  recipients,
  regions,
  storedSealed,
  switchedNotice,
  replacing,
  onReplace,
}: SecretEncryptionPanelProps) {
  const { t } = useTranslation(["checks"]);
  const browserDisabled = blocker !== null;
  const blockerText =
    blocker === "insecureContext"
      ? t("checks:sealing.blocker.insecureContext")
      : blocker === "sharedRegion"
        ? t("checks:sealing.blocker.sharedRegion")
        : undefined;
  const regionKey = recipients.some((r) => r.kind === "region");
  const agentCount = recipients.filter((r) => r.kind === "agent").length;

  return (
    <div className="space-y-2" data-testid="secret-encryption-panel" data-mode={mode}>
      <Label>{t("checks:sealing.label")}</Label>
      <div className="flex flex-wrap gap-2">
        {!required && (
          <Button
            type="button"
            size="sm"
            variant={mode === "server" ? "default" : "outline"}
            onClick={() => onModeChange("server")}
            data-testid="secret-encryption-mode-server"
            aria-pressed={mode === "server"}
          >
            <Server className="h-4 w-4" />
            {t("checks:sealing.modeServer")}
          </Button>
        )}
        <Button
          type="button"
          size="sm"
          variant={mode === "browser" ? "default" : "outline"}
          onClick={() => onModeChange("browser")}
          disabled={browserDisabled}
          title={blockerText}
          data-testid="secret-encryption-mode-browser"
          aria-pressed={mode === "browser"}
        >
          <Lock className="h-4 w-4" />
          {t("checks:sealing.modeBrowser")}
        </Button>
      </div>
      {blockerText && (
        <p className="text-xs text-muted-foreground" data-testid="secret-encryption-blocker">
          {blockerText}
        </p>
      )}
      {switchedNotice && (
        <Alert variant="warning" data-testid="secret-encryption-switched-notice">
          <AlertDescription>{t("checks:sealing.switchedToServer")}</AlertDescription>
        </Alert>
      )}
      {mode === "browser" && !browserDisabled && (
        <div className="space-y-1">
          {recipients.length === 0 ? (
            <Alert variant="warning" data-testid="sealed-secret-no-recipient">
              <AlertDescription>
                {t("checks:sealing.noRecipient", { regions: regions.join(", ") })}
              </AlertDescription>
            </Alert>
          ) : (
            <details data-testid="sealed-secret-recipients">
              <summary className="cursor-pointer list-none">
                <Badge variant="secondary" data-testid="sealed-secret-badge">
                  <Lock className="h-3 w-3" />
                  {regionKey
                    ? t("checks:sealing.badgeRegionKey", { regions: regions.join(", ") })
                    : t("checks:sealing.badgeAgents", { regions: regions.join(", "), count: agentCount })}
                </Badge>
              </summary>
              <ul className="mt-1 space-y-0.5 text-xs text-muted-foreground">
                {recipients.map((recipient) => (
                  <li key={recipient.fingerprint} data-testid="sealed-secret-recipient">
                    {recipient.kind === "region"
                      ? t("checks:sealing.recipientRegion", { region: recipient.region })
                      : (recipient.agentName ?? recipient.agentUid)}{" "}
                    <code>{recipient.fingerprint}</code>
                  </li>
                ))}
              </ul>
            </details>
          )}
          {storedSealed && !replacing && (
            <div className="flex flex-wrap items-center gap-2">
              <p className="text-xs text-muted-foreground" data-testid="sealed-secret-stored">
                {t("checks:sealing.stored")}
              </p>
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={onReplace}
                data-testid="sealed-secret-replace-button"
              >
                {t("checks:sealing.replace")}
              </Button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
