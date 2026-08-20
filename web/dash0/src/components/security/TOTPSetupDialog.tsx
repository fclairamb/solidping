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

interface TOTPSetupDialogProps {
  open: boolean;
  onClose: () => void;
  onComplete: () => void;
}

// Two-step modal: first show the QR/manual key, then prompt for the
// 6-digit verification code. On success the recovery-codes dialog
// takes over via a state hand-off.
export function TOTPSetupDialog({ open, onClose, onComplete }: TOTPSetupDialogProps) {
  const { t } = useTranslation(["account", "common"]);
  const [step, setStep] = useState<"setup" | "verify" | "recovery">("setup");
  const [secret, setSecret] = useState("");
  const [otpauthUri, setOtpauthUri] = useState("");
  const [qrDataUrl, setQrDataUrl] = useState("");
  const [code, setCode] = useState("");
  const [recoveryCodes, setRecoveryCodes] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Sentinel, not a message: resolved through t() at render time so this
  // effect never depends on `t` (whose identity changes on language switch
  // / namespace load, which would re-fire the effect and rotate the secret
  // via a second setup2FA() call).
  const [setupFailed, setSetupFailed] = useState(false);

  useEffect(() => {
    if (!open || step !== "setup") return;
    setLoading(true);
    setSetupFailed(false);
    setup2FA()
      .then((resp) => {
        if (!resp.secret && !resp.uri) {
          setSetupFailed(true);
          return;
        }
        setSecret(resp.secret);
        setOtpauthUri(resp.uri);
      })
      .catch((err) => setError(err instanceof Error ? err.message : "failed"))
      .finally(() => setLoading(false));
  }, [open, step]);

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
      const resp = await confirm2FA(code);
      setRecoveryCodes(resp.recoveryCodes);
      setStep("recovery");
    } catch (err) {
      if (err instanceof ApiError) {
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
          setStep("setup");
          setCode("");
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
          <DialogDescription>{t("account:security.totp.setupSubtitle")}</DialogDescription>
        </DialogHeader>

        {(error || setupFailed) && (
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
                  alt="otpauth QR"
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
      </DialogContent>
    </Dialog>
  );
}
