import { useEffect, useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Label } from "@/components/ui/label";
import { PasswordInput } from "@/components/ui/password-input";
import { AlertCircle, KeyRound, Loader2, Lock, Trash2, ShieldCheck } from "lucide-react";
import { toast } from "sonner";
import {
  startRegistration,
  browserSupportsWebAuthn,
} from "@simplewebauthn/browser";
import {
  beginPasskeyRegistration,
  finishPasskeyRegistration,
  listPasskeys,
  deletePasskey,
  renamePasskey,
  type PasskeyInfo,
} from "@/api/passkeys";
import { disable2FA } from "@/api/twofa";
import { TOTPSetupDialog } from "@/components/security/TOTPSetupDialog";
import { TOTPDisableDialog } from "@/components/security/TOTPDisableDialog";
import { changePassword } from "@/api/password";
import { ApiError, apiFetch } from "@/api/client";

const MIN_PASSWORD_LENGTH = 8;

export const Route = createFileRoute("/orgs/$org/account/security")({
  component: SecurityPage,
});

interface MeSecurity {
  totpEnabled: boolean;
  passkeyCount: number;
  hasPassword: boolean;
}

/**
 * Password card — "Change password" for an account that already has one,
 * "Set a password" for an SSO-only account (`hasPassword === false`), which is
 * the only way such a user ever escapes their identity provider.
 */
function PasswordCard({
  hasPassword,
  onChanged,
}: {
  hasPassword: boolean;
  onChanged: () => void;
}) {
  const { t } = useTranslation(["account", "common"]);
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [currentError, setCurrentError] = useState<string | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setCurrentError(null);
    setFormError(null);

    if (next.length < MIN_PASSWORD_LENGTH) {
      setFormError(t("account:security.password.tooShort", { min: MIN_PASSWORD_LENGTH }));
      return;
    }
    if (next !== confirm) {
      setFormError(t("account:security.password.mismatch"));
      return;
    }

    setSaving(true);
    try {
      await changePassword(next, hasPassword ? current : undefined);
      toast.success(t("account:security.password.changed"));
      setCurrent("");
      setNext("");
      setConfirm("");
      onChanged();
    } catch (err) {
      // A wrong current password belongs on its field, not in a page-level
      // Alert — the user needs to know *which* box to retype.
      if (err instanceof ApiError && err.code === "INVALID_CURRENT_PASSWORD") {
        setCurrentError(t("account:security.password.invalidCurrent"));
      } else {
        setFormError(err instanceof Error ? err.message : "failed");
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Lock className="h-5 w-5" />{" "}
          {hasPassword
            ? t("account:security.password.title")
            : t("account:security.password.setTitle")}
        </CardTitle>
        <CardDescription>
          {hasPassword
            ? t("account:security.password.subtitle")
            : t("account:security.password.setSubtitle")}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={onSubmit} className="max-w-sm space-y-4">
          {formError && (
            <Alert variant="destructive" data-testid="password-form-error">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>{formError}</AlertDescription>
            </Alert>
          )}
          {hasPassword && (
            <div className="space-y-2">
              <Label htmlFor="password-current">
                {t("account:security.password.current")}
              </Label>
              <PasswordInput
                id="password-current"
                data-testid="password-current-input"
                autoComplete="current-password"
                value={current}
                onChange={(e) => setCurrent(e.target.value)}
                disabled={saving}
                aria-invalid={currentError ? true : undefined}
              />
              {currentError && (
                <p
                  className="text-sm text-destructive"
                  data-testid="password-current-error"
                >
                  {currentError}
                </p>
              )}
            </div>
          )}
          <div className="space-y-2">
            <Label htmlFor="password-new">{t("account:security.password.new")}</Label>
            <PasswordInput
              id="password-new"
              data-testid="password-new-input"
              autoComplete="new-password"
              value={next}
              onChange={(e) => setNext(e.target.value)}
              disabled={saving}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="password-confirm">
              {t("account:security.password.confirm")}
            </Label>
            <PasswordInput
              id="password-confirm"
              data-testid="password-confirm-input"
              autoComplete="new-password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
              disabled={saving}
            />
          </div>
          <Button
            type="submit"
            data-testid="password-submit-button"
            disabled={saving}
            className="w-full sm:w-auto"
          >
            {saving ? (
              <>
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                {t("common:saving")}
              </>
            ) : hasPassword ? (
              t("account:security.password.submit")
            ) : (
              t("account:security.password.setSubmit")
            )}
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}

function SecurityPage() {
  const { t } = useTranslation(["account", "common"]);
  const [me, setMe] = useState<MeSecurity | null>(null);
  const [passkeys, setPasskeys] = useState<PasskeyInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [adding, setAdding] = useState(false);
  const [showSetup, setShowSetup] = useState(false);
  const [showDisable, setShowDisable] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const refreshAll = async () => {
    setLoading(true);
    try {
      const meResp = await apiFetch<MeSecurity>("/api/v1/auth/me");
      setMe(meResp);
      const list = await listPasskeys();
      setPasskeys(list.data);
    } catch (err) {
      setError(err instanceof Error ? err.message : "failed");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    refreshAll();
  }, []);

  const onAddPasskey = async () => {
    if (!browserSupportsWebAuthn()) {
      toast.error(t("account:security.passkeys.unsupported"));
      return;
    }
    setAdding(true);
    setError(null);
    try {
      const begin = await beginPasskeyRegistration();
      // The Go server returns the options under `options` directly; the
      // simplewebauthn-browser API expects `optionsJSON` to be the
      // PublicKeyCredentialCreationOptionsJSON that the server's
      // `protocol.CredentialCreation` marshals to.
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const optionsJSON = (begin.options as any).publicKey ?? begin.options;
      const credential = await startRegistration({ optionsJSON });
      await finishPasskeyRegistration(begin.session, credential, "");
      toast.success(t("account:security.passkeys.added"));
      refreshAll();
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message);
      } else if (err instanceof Error) {
        // NotAllowedError fires when the user cancels — treat as silent.
        if (err.name !== "NotAllowedError") {
          setError(err.message);
        }
      }
    } finally {
      setAdding(false);
    }
  };

  const onRemovePasskey = async (uid: string) => {
    try {
      await deletePasskey(uid);
      toast.success(t("account:security.passkeys.removed"));
      refreshAll();
    } catch (err) {
      if (err instanceof ApiError && err.code === "PASSKEY_LAST_AUTH_METHOD") {
        toast.error(t("account:security.passkeys.lastAuthMethod"));
      } else {
        toast.error(err instanceof Error ? err.message : "failed");
      }
    }
  };

  const onRenamePasskey = async (uid: string, current: string) => {
    const next = window.prompt(t("account:security.passkeys.renamePrompt"), current);
    if (!next || next.trim() === "" || next === current) return;
    try {
      await renamePasskey(uid, next.trim());
      refreshAll();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "failed");
    }
  };

  const onDisable2FA = async (code: string) => {
    await disable2FA(code);
    toast.success(t("account:security.totp.disabled"));
    setShowDisable(false);
    refreshAll();
  };

  if (loading || !me) {
    return (
      <div className="flex h-32 items-center justify-center">
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      {error && (
        <Alert variant="destructive">
          <AlertCircle className="h-4 w-4" />
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}

      <PasswordCard hasPassword={me.hasPassword} onChanged={refreshAll} />

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <KeyRound className="h-5 w-5" /> {t("account:security.passkeys.title")}
          </CardTitle>
          <CardDescription>{t("account:security.passkeys.subtitle")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          {passkeys.length === 0 && (
            <p className="text-sm text-muted-foreground">
              {t("account:security.passkeys.empty")}
            </p>
          )}
          {passkeys.map((p) => (
            <div
              key={p.uid}
              data-testid={`passkey-row-${p.uid}`}
              className="flex items-center justify-between rounded-md border p-3"
            >
              <div className="space-y-0.5">
                <div className="font-medium">{p.name}</div>
                <div className="text-xs text-muted-foreground">
                  {p.aaguidLabel ?? "Security key"} ·{" "}
                  {t("account:security.passkeys.added")}{" "}
                  {new Date(p.createdAt).toLocaleDateString()}
                  {p.lastUsedAt
                    ? ` · ${t("account:security.passkeys.lastUsed")} ${new Date(
                        p.lastUsedAt,
                      ).toLocaleDateString()}`
                    : ""}
                </div>
              </div>
              <div className="flex gap-2">
                <Button
                  variant="ghost"
                  size="sm"
                  data-testid={`passkey-rename-${p.uid}`}
                  onClick={() => onRenamePasskey(p.uid, p.name)}
                >
                  {t("common:rename")}
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  data-testid={`passkey-remove-${p.uid}`}
                  onClick={() => onRemovePasskey(p.uid)}
                >
                  <Trash2 className="h-4 w-4" />
                </Button>
              </div>
            </div>
          ))}
          <div>
            <Button
              data-testid="passkey-add-button"
              onClick={onAddPasskey}
              disabled={adding}
            >
              {adding ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  {t("common:saving")}
                </>
              ) : (
                t("account:security.passkeys.add")
              )}
            </Button>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <ShieldCheck className="h-5 w-5" /> {t("account:security.totp.title")}
          </CardTitle>
          <CardDescription>{t("account:security.totp.subtitle")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <div className="flex items-center justify-between">
            <div className="space-y-1">
              <div className="text-sm">
                {t("account:security.totp.status")}:{" "}
                <Badge
                  data-testid="2fa-status"
                  variant={me.totpEnabled ? "default" : "secondary"}
                >
                  {me.totpEnabled
                    ? t("account:security.totp.enabled")
                    : t("account:security.totp.notEnabled")}
                </Badge>
              </div>
              {me.totpEnabled && me.passkeyCount > 0 && (
                <p className="text-xs text-muted-foreground">
                  {t("account:security.totp.skippedWithPasskey")}
                </p>
              )}
            </div>
            {me.totpEnabled ? (
              <Button
                variant="destructive"
                data-testid="2fa-disable-button"
                onClick={() => setShowDisable(true)}
              >
                {t("account:security.totp.disable")}
              </Button>
            ) : (
              <Button
                data-testid="2fa-enable-button"
                onClick={() => setShowSetup(true)}
              >
                {t("account:security.totp.enable")}
              </Button>
            )}
          </div>
        </CardContent>
      </Card>

      {showSetup && (
        <TOTPSetupDialog
          open={showSetup}
          onClose={() => setShowSetup(false)}
          onComplete={() => {
            setShowSetup(false);
            refreshAll();
          }}
        />
      )}
      {showDisable && (
        <TOTPDisableDialog
          open={showDisable}
          onClose={() => setShowDisable(false)}
          onConfirm={onDisable2FA}
        />
      )}
    </div>
  );
}
