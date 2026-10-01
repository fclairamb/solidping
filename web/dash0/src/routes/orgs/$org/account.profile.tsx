import { useState, useEffect } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { AlertCircle, Check, ListChecks, Loader2 } from "lucide-react";
import { toast } from "sonner";
import { ApiError, apiFetch } from "@/api/client";
import {
  onboardingUiStateKey,
  useChangeEmail,
  useDeleteUiState,
  useUpdateProfile,
} from "@/api/hooks";
import { useAuth } from "@/contexts/AuthContext";

export const Route = createFileRoute("/orgs/$org/account/profile")({
  component: ProfilePage,
});

function ProfilePage() {
  return (
    <div className="space-y-6">
      <ProfileCard />
      <EmailCard />
      <OnboardingChecklistPreference />
    </div>
  );
}

function ProfileCard() {
  const { t } = useTranslation("account");
  const { t: tc } = useTranslation("common");
  const { user, refreshUser } = useAuth();
  const updateProfile = useUpdateProfile();

  const [name, setName] = useState(user?.name || "");
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    if (user) {
      setName(user.name || "");
    }
  }, [user]);

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSaved(false);

    try {
      await updateProfile.mutateAsync({ name });
      await refreshUser();
      setSaved(true);
      setTimeout(() => setSaved(false), 3000);
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message);
      } else {
        setError(tc("unexpectedError"));
      }
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("profile.title")}</CardTitle>
        <CardDescription>
          {t("profile.subtitle")}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={handleSave} className="space-y-4">
          {error && (
            <Alert variant="destructive">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}

          {saved && (
            <Alert>
              <Check className="h-4 w-4" />
              <AlertDescription>{t("profile.saved")}</AlertDescription>
            </Alert>
          )}

          <div className="space-y-2">
            <Label htmlFor="name">{t("profile.name")}</Label>
            <Input
              id="name"
              type="text"
              placeholder={t("profile.namePlaceholder")}
              value={name}
              onChange={(e) => setName(e.target.value)}
              disabled={updateProfile.isPending}
            />
          </div>

          <Button type="submit" disabled={updateProfile.isPending}>
            {updateProfile.isPending ? (
              <>
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                {tc("saving")}
              </>
            ) : (
              tc("save")
            )}
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}

/**
 * Shows the sign-in email and changes it (spec 2026-09-30-08). The server
 * asks for the current password, un-verifies the new address, signs every
 * other session out and notifies the old address. Accounts without a password
 * (SSO-only) change their email at their identity provider instead.
 */
function EmailCard() {
  const { t } = useTranslation("account");
  const { t: tc } = useTranslation("common");
  const { user, refreshUser } = useAuth();
  const changeEmail = useChangeEmail();
  // A dedicated key: this only needs hasPassword, and must not share a cache
  // entry with the security page's own /auth/me read.
  const { data: me } = useQuery({
    queryKey: ["profileEmailMe"],
    queryFn: () => apiFetch<{ hasPassword: boolean }>("/api/v1/auth/me"),
  });

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  const readOnly = Boolean(user?.isDemo || user?.impersonation);
  const hasPassword = me?.hasPassword !== false;

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSaved(false);

    try {
      await changeEmail.mutateAsync({ email, currentPassword: password });
      await refreshUser();
      setEmail("");
      setPassword("");
      setSaved(true);
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 409) {
          setError(t("email.errors.conflict"));
        } else if (err.code === "INVALID_CURRENT_PASSWORD") {
          setError(t("email.errors.wrongPassword"));
        } else if (err.status === 400) {
          setError(t("email.errors.invalid"));
        } else {
          setError(err.message);
        }
      } else {
        setError(tc("unexpectedError"));
      }
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("email.title")}</CardTitle>
        <CardDescription>{t("email.subtitle")}</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="space-y-1">
          <p className="text-sm text-muted-foreground">{t("email.current")}</p>
          <p className="break-all font-medium" data-testid="profile-current-email">
            {user?.email}
          </p>
        </div>

        {saved && (
          <Alert>
            <Check className="h-4 w-4" />
            <AlertDescription data-testid="profile-email-saved">
              {t("email.saved")}
            </AlertDescription>
          </Alert>
        )}

        {readOnly ? null : !hasPassword ? (
          <p className="text-sm text-muted-foreground" data-testid="profile-email-sso">
            {t("email.ssoOnly")}
          </p>
        ) : (
          <form onSubmit={handleSubmit} className="space-y-4">
            {error && (
              <Alert variant="destructive">
                <AlertCircle className="h-4 w-4" />
                <AlertDescription data-testid="profile-email-error">
                  {error}
                </AlertDescription>
              </Alert>
            )}

            <div className="space-y-2">
              <Label htmlFor="new-email">{t("email.newEmail")}</Label>
              <Input
                id="new-email"
                type="email"
                autoComplete="email"
                required
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                disabled={changeEmail.isPending}
                data-testid="profile-new-email"
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="email-current-password">
                {t("email.currentPassword")}
              </Label>
              <Input
                id="email-current-password"
                type="password"
                autoComplete="current-password"
                required
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                disabled={changeEmail.isPending}
                data-testid="profile-email-password"
              />
            </div>

            <p className="text-sm text-muted-foreground">{t("email.notice")}</p>

            <Button
              type="submit"
              disabled={changeEmail.isPending || !email || !password}
              className="w-full sm:w-auto"
              data-testid="profile-change-email"
            >
              {changeEmail.isPending ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  {tc("saving")}
                </>
              ) : (
                t("email.submit")
              )}
            </Button>
          </form>
        )}
      </CardContent>
    </Card>
  );
}

/**
 * Re-enables the dashboard's getting-started checklist for the organization
 * currently in the URL (spec 2026-08-28-17).
 *
 * The dismissal is a per-user, per-org server-side entry, so this is the one
 * place that can undo it — clearing browser storage would not, and neither
 * would signing in elsewhere. Deliberately not a destructive action: it
 * restores something, so no red, no trash bin.
 */
function OnboardingChecklistPreference() {
  const { t } = useTranslation("account");
  const { org } = Route.useParams();
  const resetChecklist = useDeleteUiState(onboardingUiStateKey(org));

  const handleRestore = async () => {
    try {
      await resetChecklist.mutateAsync();
      toast.success(t("onboardingChecklist.restored", { org }));
    } catch (err) {
      toast.error(
        err instanceof ApiError ? err.message : t("onboardingChecklist.failed"),
      );
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("onboardingChecklist.title")}</CardTitle>
        <CardDescription>{t("onboardingChecklist.subtitle")}</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <p className="text-sm text-muted-foreground">
          {t("onboardingChecklist.body", { org })}
        </p>
        <Button
          variant="outline"
          onClick={handleRestore}
          disabled={resetChecklist.isPending}
          className="shrink-0"
          data-testid="restore-onboarding-checklist"
        >
          {resetChecklist.isPending ? (
            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
          ) : (
            <ListChecks className="mr-2 h-4 w-4" />
          )}
          {t("onboardingChecklist.cta")}
        </Button>
      </CardContent>
    </Card>
  );
}
