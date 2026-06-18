import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "@tanstack/react-router";
import { CheckCircle2, Loader2, Router as RouterIcon } from "lucide-react";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { toast } from "sonner";

import {
  useFreeboxPairingStatus,
  useStartFreeboxPairing,
} from "@/api/hooks";

const DEFAULT_BASE_URL = "http://mafreebox.freebox.fr";

// Terminal statuses surface immediately to the user and stop polling.
const TERMINAL = new Set(["granted", "denied", "timeout"]);

interface FreeboxFormProps {
  org: string;
  /** Called after a successful pairing, with the new connection UID. */
  onPaired?: (connectionUid: string) => void;
  /** Called when the user clicks Cancel from the LCD step. */
  onCancel?: () => void;
}

type Phase = "input" | "polling" | "granted" | "denied" | "timeout" | "error";

/**
 * FreeboxForm owns the two-phase Freebox pairing UX:
 *
 *  1. The user enters a device label + base URL and clicks "Pair with Freebox".
 *     We POST /integrations/freebox/pair which creates the channel in
 *     `pairing` state and asks the Freebox for an app_token.
 *  2. The Freebox LCD shows a pairing prompt; the user has ~30 s to press
 *     the right-arrow key. We poll the status endpoint every ~2 s.
 *
 * On `granted` the form navigates to the channel detail page (via the
 * `onPaired` callback). `denied` / `timeout` show a retry-in-place
 * error.
 */
export function FreeboxForm({ org, onPaired, onCancel }: FreeboxFormProps) {
  const { t } = useTranslation("integrations");
  const navigate = useNavigate();

  const [name, setName] = useState("Freebox");
  const [baseUrl, setBaseUrl] = useState(DEFAULT_BASE_URL);
  const [phase, setPhase] = useState<Phase>("input");
  const [connectionUid, setConnectionUid] = useState<string | undefined>();
  const [errorMessage, setErrorMessage] = useState<string | undefined>();

  const start = useStartFreeboxPairing(org);

  // Derive the current phase from the polling result rather than
  // mirroring it into a separate setState — keeps the effect-free
  // rendering contract React 19's linter enforces.
  const polling = phase === "polling";
  const statusQuery = useFreeboxPairingStatus(org, connectionUid, polling);
  const effectivePhase = useMemo<Phase>(() => {
    if (!polling) return phase;
    const next = statusQuery.data?.status;
    if (next && TERMINAL.has(next)) {
      return next as Phase;
    }

    return phase;
  }, [phase, polling, statusQuery.data]);

  // Auto-navigate once the operator has approved.
  useEffect(() => {
    if (effectivePhase !== "granted" || !connectionUid) return;

    toast.success(t("freebox.paired", "Paired — connection ready"));

    if (onPaired) {
      onPaired(connectionUid);

      return;
    }

    navigate({
      to: "/orgs/$org/integrations/$integrationUid",
      params: { org, integrationUid: connectionUid },
    });
  }, [effectivePhase, connectionUid, navigate, onPaired, org, t]);

  const handlePair = async (e: React.FormEvent) => {
    e.preventDefault();
    if (start.isPending) return;

    try {
      const resp = await start.mutateAsync({
        name: name || "Freebox",
        baseUrl: baseUrl || DEFAULT_BASE_URL,
      });
      setConnectionUid(resp.connectionUid);
      setPhase("polling");
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err);
      setErrorMessage(msg);
      setPhase("error");
    }
  };

  const handleRetry = () => {
    // Reset to the input phase; the previous (failed) channel row is
    // kept in the DB but the user will create a new one. We could also
    // call DELETE on it — leaving the audit trail wins for now.
    setConnectionUid(undefined);
    setErrorMessage(undefined);
    setPhase("input");
  };

  if (effectivePhase === "input" || effectivePhase === "error") {
    return (
      <form onSubmit={handlePair} className="space-y-4">
        <div className="space-y-2">
          <Label htmlFor="freebox-name">
            {t("freebox.deviceName", "Device label")}
          </Label>
          <Input
            id="freebox-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Living-room Freebox"
            data-testid="freebox-name"
          />
          <p className="text-xs text-muted-foreground">
            {t(
              "freebox.deviceNameHint",
              "Shown on the Freebox admin under Authorized apps. Pick something you'll recognise later.",
            )}
          </p>
        </div>

        <div className="space-y-2">
          <Label htmlFor="freebox-url">
            {t("freebox.baseUrl", "Freebox base URL")}
          </Label>
          <Input
            id="freebox-url"
            type="url"
            value={baseUrl}
            onChange={(e) => setBaseUrl(e.target.value)}
            placeholder={DEFAULT_BASE_URL}
            data-testid="freebox-url"
          />
          <p className="text-xs text-muted-foreground">
            {t(
              "freebox.baseUrlHint",
              "Leave as-is if SolidPing runs on the same LAN as the Freebox. For remote access, use the public hostname you configured in the Freebox admin under Settings → Freebox OS API.",
            )}
          </p>
        </div>

        {effectivePhase === "error" && (
          <Alert variant="destructive">
            <AlertTitle>
              {t("freebox.pairingFailed", "Could not reach the Freebox.")}
            </AlertTitle>
            <AlertDescription>{errorMessage}</AlertDescription>
          </Alert>
        )}

        <div className="flex justify-end gap-2">
          {onCancel && (
            <Button
              type="button"
              variant="outline"
              onClick={onCancel}
              disabled={start.isPending}
            >
              {t("cancel", "Cancel")}
            </Button>
          )}
          <Button
            type="submit"
            disabled={start.isPending}
            data-testid="freebox-pair-button"
          >
            {start.isPending ? (
              <>
                <Loader2 className="h-4 w-4 mr-1 animate-spin" />
                {t("freebox.pairing", "Starting pairing…")}
              </>
            ) : (
              <>
                <RouterIcon className="h-4 w-4 mr-1" />
                {t("freebox.pairButton", "Pair with Freebox")}
              </>
            )}
          </Button>
        </div>
      </form>
    );
  }

  if (effectivePhase === "polling") {
    return (
      <PairingPrompt
        title={t("freebox.pressArrow", "Press → on your Freebox LCD")}
        description={t(
          "freebox.waitingApproval",
          "Waiting for LCD approval…",
        )}
        onCancel={handleRetry}
        cancelLabel={t("cancel", "Cancel")}
      />
    );
  }

  if (effectivePhase === "granted") {
    return (
      <div className="flex items-center gap-3 rounded border bg-muted/30 p-4">
        <CheckCircle2 className="h-5 w-5 text-green-600" />
        <p className="text-sm">
          {t("freebox.paired", "Paired — connection ready")}
        </p>
      </div>
    );
  }

  // denied / timeout
  return (
    <PairingFailure
      phase={effectivePhase as "denied" | "timeout"}
      onRetry={handleRetry}
      retryLabel={t("freebox.pairButton", "Pair with Freebox")}
    />
  );
}

interface PairingPromptProps {
  title: string;
  description: string;
  onCancel: () => void;
  cancelLabel: string;
}

function PairingPrompt({ title, description, onCancel, cancelLabel }: PairingPromptProps) {
  return (
    <div
      className="space-y-3 rounded border bg-muted/30 p-4"
      data-testid="freebox-polling"
    >
      <div className="flex items-center gap-3">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
        <div>
          <p className="font-medium text-sm">{title}</p>
          <p className="text-xs text-muted-foreground">{description}</p>
        </div>
      </div>
      <div className="flex justify-end">
        <Button type="button" variant="outline" size="sm" onClick={onCancel}>
          {cancelLabel}
        </Button>
      </div>
    </div>
  );
}

interface PairingFailureProps {
  phase: "denied" | "timeout";
  onRetry: () => void;
  retryLabel: string;
}

function PairingFailure({ phase, onRetry, retryLabel }: PairingFailureProps) {
  const { t } = useTranslation("integrations");

  const message = useMemo(() => {
    if (phase === "timeout") {
      return t(
        "freebox.pairingTimeout",
        "The Freebox LCD prompt expired before approval. Try again.",
      );
    }

    return t("freebox.pairingDenied", "The pairing was rejected on the Freebox.");
  }, [phase, t]);

  return (
    <div className="space-y-3" data-testid={`freebox-${phase}`}>
      <Alert variant="destructive">
        <AlertDescription>{message}</AlertDescription>
      </Alert>
      <div className="flex justify-end">
        <Button type="button" onClick={onRetry}>
          {retryLabel}
        </Button>
      </div>
    </div>
  );
}
