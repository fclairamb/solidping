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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
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
import { getImpersonation } from "@/lib/impersonation";
import {
  LANGUAGES,
  browserTimezone,
  resolveLang,
  supportedLanguage,
  type LanguageCode,
} from "@/lib/languages";

export const Route = createFileRoute("/orgs/$org/account/profile")({
  component: ProfilePage,
});

function ProfilePage() {
  return (
    <div className="space-y-6">
      <ProfileCard />
      <EmailCard />
      <EmailLanguageCard />
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

  // Keyed on the saved name, not the user object: saving the email language
  // or the timezone replaces the user too, and must not wipe a name being
  // typed.
  const savedName = user ? user.name || "" : null;
  useEffect(() => {
    if (savedName !== null) {
      setName(savedName);
    }
  }, [savedName]);

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
 * The language the server writes this user's emails in (spec 2026-10-21-01).
 * It is the dashboard language too: picking one here switches both, exactly
 * like the sidebar's language menu. Dates in emails follow the browser's
 * timezone, which AuthProvider keeps saved; it is shown, not chosen.
 *
 * Demo and impersonation sessions neither adopt nor write these (see
 * preferencesSyncMode): the card shows the account's saved values as the
 * server uses them (an empty field is English, in UTC), read-only, and says
 * so instead of tying them to this dashboard and browser.
 */
function EmailLanguageCard() {
  const { t, i18n } = useTranslation("account");
  const { t: tc } = useTranslation("common");
  const { user, changeLanguage } = useAuth();

  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState(false);

  const impersonating = Boolean(user?.impersonation) || getImpersonation() !== null;
  const readOnly = impersonating || Boolean(user?.isDemo);
  const current: LanguageCode =
    supportedLanguage(user?.preferences?.locale) ??
    (readOnly ? "en" : resolveLang(i18n.resolvedLanguage));
  const timezone =
    user?.preferences?.timezone || (readOnly ? undefined : browserTimezone()) || "UTC";
  let subtitle = t("emailLanguage.subtitle");
  if (impersonating) {
    subtitle = t("emailLanguage.impersonating");
  } else if (readOnly) {
    subtitle = t("emailLanguage.demo");
  }

  const handleChange = async (value: string) => {
    const code = supportedLanguage(value);
    if (!code || code === current) {
      return;
    }
    setSaving(true);
    setSaved(false);
    setError(false);
    try {
      await changeLanguage(code);
      setSaved(true);
    } catch {
      setError(true);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card data-testid="profile-email-language-card">
      <CardHeader>
        <CardTitle data-testid="profile-email-language-title">
          {t("emailLanguage.title")}
        </CardTitle>
        <CardDescription data-testid="profile-email-language-subtitle">
          {subtitle}
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {saved && (
          <Alert data-testid="profile-email-language-saved-alert">
            <Check className="h-4 w-4" />
            <AlertDescription data-testid="profile-email-language-saved">
              {t("emailLanguage.saved")}
            </AlertDescription>
          </Alert>
        )}
        {error && (
          <Alert variant="destructive" data-testid="profile-email-language-error-alert">
            <AlertCircle className="h-4 w-4" />
            <AlertDescription data-testid="profile-email-language-error">
              {t("emailLanguage.failed")}
            </AlertDescription>
          </Alert>
        )}

        <div className="space-y-2">
          <Label htmlFor="email-language" data-testid="profile-email-language-label">
            {tc("language")}
          </Label>
          <Select
            value={current}
            onValueChange={(value) => void handleChange(value)}
            disabled={readOnly || saving}
          >
            <SelectTrigger
              id="email-language"
              className="w-full sm:max-w-xs"
              data-testid="profile-email-language-select"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {LANGUAGES.map(({ code, flag, label }) => (
                <SelectItem
                  key={code}
                  value={code}
                  data-testid={`profile-email-language-option-${code}`}
                >
                  <span className="text-base">{flag}</span> {label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <p className="text-sm text-muted-foreground" data-testid="profile-email-timezone">
          {readOnly
            ? t("emailLanguage.timezoneSaved", { timezone })
            : t("emailLanguage.timezone", { timezone })}
        </p>
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
