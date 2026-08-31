import { useState } from "react";
import { useTranslation } from "react-i18next";
import { KeyRound, Monitor, RefreshCw, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { ApiError } from "@/api/client";
import {
  useGenerateKioskToken,
  useRevokeKioskToken,
  type StatusPage,
} from "@/api/hooks";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  CopyableCode,
  CopyableInline,
} from "@/components/shared/copyable-code";

/**
 * TV mode card for a status page (spec 2026-08-29-08).
 *
 * Two things an operator needs and nothing else: the URL to type into the
 * screen, and — for a page that is not public — the kiosk token that lets that
 * screen render unattended.
 *
 * The token is shown ONCE, at mint time, like an API key: only its sha256 is
 * stored, so there is nothing to show later. That is stated on the card rather
 * than discovered, because the failure mode ("I'll copy it tomorrow") is
 * silent and only surfaces when someone is standing in front of a blank TV.
 */
export function StatusPageTvCard({
  org,
  page,
}: {
  org: string;
  page: StatusPage;
}) {
  const { t } = useTranslation(["statusPages", "common"]);

  const [mintedToken, setMintedToken] = useState<string | null>(null);
  const [regenerateOpen, setRegenerateOpen] = useState(false);
  const [revokeOpen, setRevokeOpen] = useState(false);

  const generate = useGenerateKioskToken(org, page.uid);
  const revoke = useRevokeKioskToken(org, page.uid);

  const isPublic = page.visibility === "public";
  const tvPath = `/status0/${org}/${page.slug}/tv`;
  const tvUrl = `${window.location.origin}${tvPath}`;

  // The URL an operator should actually paste. While a token is on screen it
  // is the tokened one — copying the bare URL now and discovering it 404s on
  // the TV later is the whole trap this card exists to avoid.
  const displayUrl = mintedToken
    ? `${tvUrl}?kiosk=${encodeURIComponent(mintedToken)}`
    : tvUrl;

  const handleGenerate = async () => {
    try {
      const result = await generate.mutateAsync();
      setMintedToken(result.token);
      toast.success(t("statusPages:tvMode.tokenGenerated"));
    } catch (err) {
      toast.error(
        err instanceof ApiError
          ? err.message
          : t("statusPages:tvMode.tokenFailed"),
      );
    }
  };

  const handleRevoke = async () => {
    try {
      await revoke.mutateAsync();
      setMintedToken(null);
      toast.success(t("statusPages:tvMode.tokenRevoked"));
    } catch (err) {
      toast.error(
        err instanceof ApiError
          ? err.message
          : t("statusPages:tvMode.tokenFailed"),
      );
    }
  };

  return (
    <Card data-testid="status-page-tv-card">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <Monitor className="h-4 w-4" />
          {t("statusPages:tvMode.title")}
        </CardTitle>
        <CardDescription>{t("statusPages:tvMode.description")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <CopyableCode code={displayUrl} data-testid="tv-mode-url" />

        {isPublic ? (
          <p
            className="text-sm text-muted-foreground"
            data-testid="tv-mode-public-note"
          >
            {t("statusPages:tvMode.publicNote")}
          </p>
        ) : (
          <div className="space-y-3">
            <p
              className="text-sm text-muted-foreground"
              data-testid="tv-mode-restricted-note"
            >
              {t("statusPages:tvMode.restrictedNote")}
            </p>

            {mintedToken && (
              <Alert data-testid="tv-mode-token-alert">
                <KeyRound />
                <AlertDescription className="space-y-2">
                  <span className="block text-sm font-medium">
                    {t("statusPages:tvMode.tokenShownOnce")}
                  </span>
                  <CopyableInline
                    value={mintedToken}
                    label={t("statusPages:tvMode.tokenLabel")}
                    size="md"
                  />
                </AlertDescription>
              </Alert>
            )}

            <div className="flex flex-wrap gap-2">
              {page.hasKioskToken ? (
                <>
                  <Button
                    variant="outline"
                    size="sm"
                    data-testid="tv-mode-regenerate"
                    onClick={() => setRegenerateOpen(true)}
                    disabled={generate.isPending}
                  >
                    <RefreshCw className="mr-2 h-4 w-4" />
                    {t("statusPages:tvMode.regenerate")}
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    className="text-destructive hover:text-destructive"
                    data-testid="tv-mode-revoke"
                    onClick={() => setRevokeOpen(true)}
                    disabled={revoke.isPending}
                  >
                    <Trash2 className="mr-2 h-4 w-4" />
                    {t("statusPages:tvMode.revoke")}
                  </Button>
                </>
              ) : (
                <Button
                  variant="outline"
                  size="sm"
                  data-testid="tv-mode-generate"
                  onClick={() => void handleGenerate()}
                  disabled={generate.isPending}
                >
                  <KeyRound className="mr-2 h-4 w-4" />
                  {t("statusPages:tvMode.generate")}
                </Button>
              )}
            </div>
          </div>
        )}
      </CardContent>

      {/* Regenerating is destructive to whatever screen is running today, so
          it asks — the button's own label cannot convey "and the TV in the
          lobby goes blank". */}
      <AlertDialog open={regenerateOpen} onOpenChange={setRegenerateOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("statusPages:tvMode.regenerateTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("statusPages:tvMode.regenerateDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common:cancel")}</AlertDialogCancel>
            <AlertDialogAction
              data-testid="tv-mode-regenerate-confirm"
              onClick={() => void handleGenerate()}
            >
              {t("statusPages:tvMode.regenerate")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={revokeOpen} onOpenChange={setRevokeOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("statusPages:tvMode.revokeTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("statusPages:tvMode.revokeDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common:cancel")}</AlertDialogCancel>
            <AlertDialogAction
              data-testid="tv-mode-revoke-confirm"
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              onClick={() => void handleRevoke()}
            >
              {t("statusPages:tvMode.revoke")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  );
}
