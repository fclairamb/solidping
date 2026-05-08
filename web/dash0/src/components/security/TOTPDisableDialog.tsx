import { useState } from "react";
import { useTranslation } from "react-i18next";
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
import { AlertCircle, Loader2 } from "lucide-react";
import { ApiError } from "@/api/client";

interface TOTPDisableDialogProps {
  open: boolean;
  onClose: () => void;
  onConfirm: (code: string) => Promise<void>;
}

// Confirms a current 6-digit code before disabling. We deliberately
// don't accept recovery codes here — disable should require live access
// to the authenticator to defend against a stolen-session attacker.
export function TOTPDisableDialog({ open, onClose, onConfirm }: TOTPDisableDialogProps) {
  const { t } = useTranslation(["account", "common"]);
  const [code, setCode] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async () => {
    setLoading(true);
    setError(null);
    try {
      await onConfirm(code);
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

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("account:security.totp.disableTitle")}</DialogTitle>
          <DialogDescription>
            {t("account:security.totp.disableSubtitle")}
          </DialogDescription>
        </DialogHeader>
        {error && (
          <Alert variant="destructive">
            <AlertCircle className="h-4 w-4" />
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <div className="space-y-2">
          <Label htmlFor="2fa-disable-code">
            {t("account:security.totp.codeLabel")}
          </Label>
          <Input
            id="2fa-disable-code"
            data-testid="2fa-disable-code"
            inputMode="numeric"
            maxLength={6}
            value={code}
            onChange={(e) => setCode(e.target.value.replace(/\D/g, ""))}
            placeholder="000000"
            className="font-mono"
          />
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common:cancel")}
          </Button>
          <Button
            variant="destructive"
            data-testid="2fa-disable-confirm"
            disabled={loading || code.length !== 6}
            onClick={submit}
          >
            {loading ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : null}
            {t("account:security.totp.disable")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
