import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import QRCode from "qrcode";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { AlertCircle, Copy, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { setup2FA, confirm2FA } from "@/api/twofa";
import { ApiError } from "@/api/client";
import { RecoveryCodesDialog } from "./RecoveryCodesDialog";
import { ReauthPasswordForm, SignInAgainAlert } from "./reauth";
import { isReauthRequired } from "./reauth-errors";

interface TOTPSetupDialogProps {
  open: boolean;
  /** The account has a password: the dialog asks for it first. */
  hasPassword: boolean;
  onClose: () => void;
  onComplete: () => void;
}

// Multi-step modal. An account with a password re-authenticates first
// (spec 2026-10-31-02): the secret is only handed out after the current
// password. Then it shows the QR/manual key and prompts for the 6-digit
// verification code. On success the recovery-codes dialog takes over via a
// state hand-off.
export function TOTPSetupDialog({ open, hasPassword, onClose, onComplete }: TOTPSetupDialogProps) {
  const { t } = useTranslation(["account", "common"]);
  const firstStep = hasPassword ? "reauth" : "setup";
  const [step, setStep] = useState<"reauth" | "setup" | "recovery">(firstStep);
  const [secret, setSecret] = useState("");
  const [setupToken, setSetupToken] = useState("");
  const [signInRequired, setSignInRequired] = useState(false);
  const [otpauthUri, setOtpauthUri] = useState("");
  const [qrDataUrl, setQrDataUrl] = useState("");
  const [code, setCode] = useState("");
  const [recoveryCodes, setRecoveryCodes] = useState<string[]>([]);
  // An account without a password fetches the secret on mount.
  const [loading, setLoading] = useState(!hasPassword);
  // Bumped to fetch a new secret after the setup token expired.
  const [attempt, setAttempt] = useState(0);
  const [error, setError] = useState<string | null>(null);
  // Sentinel, not a message: resolved through t() at render time so this
  // effect never depends on `t` (whose identity changes on language switch
  // / namespace load, which would re-fire the effect and rotate the secret
  // via a second setup2FA() call).
  const [setupFailed, setSetupFailed] = useState(false);

  const applySetup = (resp: Awaited<ReturnType<typeof setup2FA>>) => {
    if (!resp.secret && !resp.uri) {
      setSetupFailed(true);
      return;
    }
    setSecret(resp.secret);
    setOtpauthUri(resp.uri);
    setSetupToken(resp.setupToken);
  };

  // An account without a password starts the setup right away: the server
  // accepts it when the session signed in recently, and otherwise answers
  // REAUTHENTICATION_REQUIRED, which shows the "sign in again" notice.
  useEffect(() => {
    if (hasPassword || !open || step !== "setup" || secret) return;
    setup2FA()
      .then(applySetup)
      .catch((err) => {
        if (isReauthRequired(err)) {
          setSignInRequired(true);
        } else {
          setError(err instanceof Error ? err.message : "failed");
        }
      })
      .finally(() => setLoading(false));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, step, attempt]);

  // The current-password step: setup2FA throws on a wrong password and the
  // form shows it.
  const onReauth = async (password: string) => {
    const resp = await setup2FA(password);
    setError(null);
    applySetup(resp);
    setStep("setup");
  };

  // Render the otpauth URI as a QR code entirely client-side — no network
  // request may leave the page with the TOTP seed.
  useEffect(() => {
    if (!otpauthUri) return;
    let cancelled = false;
    QRCode.toDataURL(otpauthUri, { width: 200, margin: 1 })
      .then((dataUrl) => {
        if (!cancelled) setQrDataUrl(dataUrl);
      })
      .catch(() => {
        if (!cancelled) setQrDataUrl("");
      });
    return () => {
      cancelled = true;
    };
  }, [otpauthUri]);

  const onConfirm = async () => {
    setLoading(true);
    setError(null);
    try {
      const resp = await confirm2FA(code, setupToken);
      setRecoveryCodes(resp.recoveryCodes);
      setStep("recovery");
    } catch (err) {
      if (err instanceof ApiError && err.code === "INVALID_SETUP_TOKEN") {
        // The setup expired (10 minutes): start over from the first step.
        setSecret("");
        setOtpauthUri("");
        setQrDataUrl("");
        setSetupToken("");
        setCode("");
        setSetupFailed(false);
        setStep(firstStep);
        setError(t("account:security.totp.setupExpired"));
        if (!hasPassword) setAttempt((n) => n + 1);
        return;
      } else if (err instanceof ApiError) {
        setError(err.message);
      } else {
        setError(err instanceof Error ? err.message : "failed");
      }
    } finally {
      setLoading(false);
    }
  };

  const copySecret = () => {
    navigator.clipboard.writeText(secret);
    toast.success(t("common:copied"));
  };

  if (step === "recovery") {
    return (
      <RecoveryCodesDialog
        open={open}
        codes={recoveryCodes}
        onClose={() => {
          setStep(firstStep);
          setCode("");
          setSetupToken("");
          setSecret("");
          setOtpauthUri("");
          setQrDataUrl("");
          setRecoveryCodes([]);
          onComplete();
        }}
      />
    );
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("account:security.totp.setupTitle")}</DialogTitle>
          <DialogDescription>
            {step === "reauth"
              ? t("account:security.reauth.subtitle")
              : t("account:security.totp.setupSubtitle")}
          </DialogDescription>
        </DialogHeader>

        {step === "reauth" && (
          <>
            {error && (
              <Alert variant="destructive">
                <AlertCircle className="h-4 w-4" />
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}
            <ReauthPasswordForm testIdPrefix="2fa" onSubmit={onReauth} onCancel={onClose} />
          </>
        )}

        {step === "setup" && signInRequired && <SignInAgainAlert />}

        {step === "setup" && (error || setupFailed) && (
          <Alert variant="destructive">
            <AlertCircle className="h-4 w-4" />
            <AlertDescription>
              {error ?? t("account:security.totp.setupFailed")}
            </AlertDescription>
          </Alert>
        )}

        {loading && step === "setup" && (
          <div className="flex h-32 items-center justify-center">
            <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
          </div>
        )}

        {!loading && step === "setup" && secret && (
          <div className="space-y-4">
            {qrDataUrl && (
              <div className="flex justify-center">
                {/* Rendered fully client-side from the otpauth:// URI (the
                    `qrcode` package) — no network request ever carries the
                    TOTP seed off the page. */}
                <img
                  data-testid="2fa-qr-code"
                  alt={t("security.totp.qrAlt")}
                  src={qrDataUrl}
                  className="rounded border"
                />
              </div>
            )}
            <div className="space-y-1">
              <Label>{t("account:security.totp.manualSecret")}</Label>
              <div className="flex items-center gap-2">
                <Input
                  data-testid="2fa-manual-secret"
                  readOnly
                  value={secret}
                  className="font-mono text-xs"
                />
                <Button
                  variant="ghost"
                  size="sm"
                  data-testid="2fa-copy-secret"
                  onClick={copySecret}
                >
                  <Copy className="h-4 w-4" />
                </Button>
              </div>
            </div>
            <div className="space-y-1">
              <Label htmlFor="2fa-setup-code">
                {t("account:security.totp.codeLabel")}
              </Label>
              <Input
                id="2fa-setup-code"
                data-testid="2fa-setup-code"
                inputMode="numeric"
                maxLength={6}
                value={code}
                onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
                placeholder="000000"
                className="font-mono"
              />
            </div>
          </div>
        )}

        {step === "setup" && (
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common:cancel")}
          </Button>
          <Button
            data-testid="2fa-setup-confirm"
            onClick={onConfirm}
            disabled={loading || code.length !== 6}
          >
            {loading ? (
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
            ) : null}
            {t("account:security.totp.confirm")}
          </Button>
        </DialogFooter>
        )}
      </DialogContent>
    </Dialog>
  );
}
