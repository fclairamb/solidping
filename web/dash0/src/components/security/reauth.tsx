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
import { Label } from "@/components/ui/label";
import { PasswordInput } from "@/components/ui/password-input";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { AlertCircle, Loader2 } from "lucide-react";
import { useReauthErrorMessage } from "./reauth-errors";
import { useAuth } from "@/contexts/AuthContext";

// Adding a sign-in factor (2FA, passkey, first password) needs a
// re-authentication (spec 2026-10-31-02): the current password when the
// account has one, otherwise a sign-in within the last 10 minutes.

/** Tells a passwordless user to sign in again, with the button that does it. */
export function SignInAgainAlert() {
  const { t } = useTranslation(["account"]);
  const { logout } = useAuth();
  return (
    <Alert variant="destructive" data-testid="reauth-sign-in-required">
      <AlertCircle className="h-4 w-4" />
      <AlertDescription className="space-y-2">
        <p>{t("account:security.reauth.signInAgainBody")}</p>
        <Button
          size="sm"
          variant="outline"
          data-testid="reauth-sign-in-again"
          onClick={() => void logout()}
        >
          {t("account:security.reauth.signInAgain")}
        </Button>
      </AlertDescription>
    </Alert>
  );
}

interface ReauthPasswordFormProps {
  /** Prefix of every data-testid, e.g. "2fa" or "passkey". */
  testIdPrefix: string;
  onSubmit: (password: string) => Promise<void>;
  onCancel: () => void;
}

/** The current-password step, shared by the dialogs that add a factor. */
export function ReauthPasswordForm({ testIdPrefix, onSubmit, onCancel }: ReauthPasswordFormProps) {
  const { t } = useTranslation(["account", "common"]);
  const reauthMessage = useReauthErrorMessage();
  const [password, setPassword] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setLoading(true);
    setError(null);
    try {
      await onSubmit(password);
    } catch (err) {
      setError(
        reauthMessage(err) ??
          (err instanceof Error ? err.message : t("account:security.reauth.failed")),
      );
    } finally {
      setLoading(false);
    }
  };

  return (
    <form onSubmit={submit} className="space-y-4" data-testid={`${testIdPrefix}-reauth-form`}>
      {error && (
        <Alert variant="destructive" data-testid={`${testIdPrefix}-reauth-error`}>
          <AlertCircle className="h-4 w-4" />
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      <div className="space-y-2">
        <Label htmlFor={`${testIdPrefix}-current-password`}>
          {t("account:security.reauth.passwordLabel")}
        </Label>
        <PasswordInput
          id={`${testIdPrefix}-current-password`}
          data-testid={`${testIdPrefix}-current-password`}
          autoComplete="current-password"
          autoFocus
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          disabled={loading}
        />
      </div>
      <DialogFooter>
        <Button
          type="button"
          variant="outline"
          data-testid={`${testIdPrefix}-reauth-cancel`}
          onClick={onCancel}
        >
          {t("common:cancel")}
        </Button>
        <Button
          type="submit"
          data-testid={`${testIdPrefix}-reauth-continue`}
          disabled={loading || password === ""}
        >
          {loading ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : null}
          {t("account:security.reauth.continue")}
        </Button>
      </DialogFooter>
    </form>
  );
}

interface ReauthPasswordDialogProps {
  open: boolean;
  title: string;
  testIdPrefix: string;
  onClose: () => void;
  onSubmit: (password: string) => Promise<void>;
}

/** A dialog holding only the current-password step. */
export function ReauthPasswordDialog({
  open,
  title,
  testIdPrefix,
  onClose,
  onSubmit,
}: ReauthPasswordDialogProps) {
  const { t } = useTranslation(["account"]);
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent data-testid={`${testIdPrefix}-reauth-dialog`}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{t("account:security.reauth.subtitle")}</DialogDescription>
        </DialogHeader>
        <ReauthPasswordForm testIdPrefix={testIdPrefix} onSubmit={onSubmit} onCancel={onClose} />
      </DialogContent>
    </Dialog>
  );
}
