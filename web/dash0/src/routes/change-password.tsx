import { useState } from "react";
import { Navigate, createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { PasswordInput } from "@/components/ui/password-input";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { AlertCircle, KeyRound, Loader2 } from "lucide-react";
import { AuthSplitLayout } from "@/components/layout/auth-split-layout";
import { changePassword } from "@/api/password";
import { ApiError, getToken } from "@/api/client";
import { useAuth } from "@/contexts/AuthContext";

const MIN_PASSWORD_LENGTH = 8;

export const Route = createFileRoute("/change-password")({
  component: ForcedPasswordChangePage,
});

/**
 * The forced-rotation screen (spec 2026-08-23-04).
 *
 * Two things land a user here, and between them the screen is inescapable:
 * AuthContext routes here on a login response (or an /auth/me restore) that
 * carries `mustChangePassword`, and `api/client.ts` bounces back here from the
 * 403 / PASSWORD_CHANGE_REQUIRED that every other endpoint answers with. So
 * navigating away — Back, a bookmark, retyping a URL — fails its first data
 * fetch and returns.
 *
 * There is deliberately no navigation chrome and no "skip" affordance. The
 * only exits are completing the rotation and signing out, which is why the
 * sign-out link is here: a session with no way out at all is its own failure
 * mode.
 *
 * The most common visitor is the seeded bootstrap admin on a brand-new
 * install, whose password is published in a public repository — hence the
 * explanatory copy rather than a bare form.
 */
function ForcedPasswordChangePage() {
  const { t } = useTranslation(["auth", "account", "common"]);
  const { user, logout } = useAuth();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);

    if (next.length < MIN_PASSWORD_LENGTH) {
      setError(t("auth:passwordTooShort"));
      return;
    }
    if (next !== confirm) {
      setError(t("account:security.password.mismatch"));
      return;
    }

    setSaving(true);
    try {
      await changePassword(next, current);
      // Full reload rather than a router navigation: the rotation revoked the
      // other sessions and cleared the server-side flag, and a hard load is
      // the simplest way to guarantee every cached query and the auth context
      // are rebuilt against the now-unblocked session.
      const basepath = import.meta.env.VITE_BASE_URL || "";
      window.location.href = `${basepath}/`;
    } catch (err) {
      if (err instanceof ApiError && err.code === "INVALID_CURRENT_PASSWORD") {
        setError(t("account:security.password.invalidCurrent"));
      } else {
        setError(err instanceof Error ? err.message : t("auth:unexpectedError"));
      }
      setSaving(false);
    }
  };

  // No session at all: nothing to rotate. Send them to sign in rather than
  // render a form whose submit can only 401.
  if (!getToken()) {
    return <Navigate to="/login" search={{ returnTo: undefined }} />;
  }

  return (
    <AuthSplitLayout>
      <Card
        className="w-full max-w-md border-t-4 border-t-brand"
        data-testid="forced-password-change"
      >
        <CardHeader className="text-center">
          <div className="flex justify-center mb-4">
            <KeyRound className="h-12 w-12 text-brand" />
          </div>
          <CardTitle className="text-2xl">{t("auth:mustChangePasswordTitle")}</CardTitle>
          <CardDescription>
            {t("auth:mustChangePasswordSubtitle")}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="space-y-4">
            {error && (
              <Alert variant="destructive" data-testid="forced-password-error">
                <AlertCircle className="h-4 w-4" />
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}

            {user?.email && (
              <p className="text-sm text-muted-foreground break-all">
                {t("auth:mustChangePasswordFor", { email: user.email })}
              </p>
            )}

            <div className="space-y-2">
              <Label htmlFor="forced-current">
                {t("account:security.password.current")}
              </Label>
              <PasswordInput
                id="forced-current"
                data-testid="forced-password-current"
                autoComplete="current-password"
                value={current}
                onChange={(e) => setCurrent(e.target.value)}
                disabled={saving}
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="forced-new">{t("account:security.password.new")}</Label>
              <PasswordInput
                id="forced-new"
                data-testid="forced-password-new"
                autoComplete="new-password"
                value={next}
                onChange={(e) => setNext(e.target.value)}
                disabled={saving}
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="forced-confirm">
                {t("account:security.password.confirm")}
              </Label>
              <PasswordInput
                id="forced-confirm"
                data-testid="forced-password-confirm"
                autoComplete="new-password"
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
                disabled={saving}
              />
            </div>

            <Button
              type="submit"
              className="w-full"
              disabled={saving}
              data-testid="forced-password-submit"
            >
              {saving ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  {t("account:security.password.submit")}
                </>
              ) : (
                t("account:security.password.submit")
              )}
            </Button>

            <div className="text-center">
              <button
                type="button"
                onClick={() => void logout()}
                className="text-sm text-muted-foreground underline-offset-4 hover:underline"
                data-testid="forced-password-signout"
              >
                {t("auth:mustChangePasswordSignOut")}
              </button>
            </div>
          </form>
        </CardContent>
      </Card>
    </AuthSplitLayout>
  );
}
