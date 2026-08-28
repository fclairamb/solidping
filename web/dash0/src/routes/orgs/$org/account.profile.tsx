import { useState, useEffect } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute } from "@tanstack/react-router";
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
import { ApiError } from "@/api/client";
import {
  onboardingUiStateKey,
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
