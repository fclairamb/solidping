import { useState, useEffect } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
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
import { AlertCircle, Check, Loader2 } from "lucide-react";
import { ApiError, markOrgDeleted } from "@/api/client";
import { useQueryClient } from "@tanstack/react-query";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  useDeleteOrg,
  useEscalationPolicies,
  useOrgSettings,
  useUpdateOrgSettings,
} from "@/api/hooks";
import { useAuth } from "@/contexts/AuthContext";
import {
  ConfirmByTypingButton,
  DangerZone,
} from "@/components/shared/danger-zone";
import { OrgProfileCard } from "@/components/shared/org-profile-card";

// Sentinel Select value for "no org default" (Radix needs a non-empty value).
const NO_DEFAULT = "__none__";

export const Route = createFileRoute("/orgs/$org/organization/settings")({
  component: SettingsPage,
});

function SettingsPage() {
  const { t } = useTranslation("org");
  const { t: tc } = useTranslation("common");
  const { org } = Route.useParams();
  const navigate = useNavigate();
  const { user, adoptOrgDeletionSession } = useAuth();
  const queryClient = useQueryClient();
  const { data: settings, isLoading } = useOrgSettings(org);
  const deleteOrg = useDeleteOrg(org);
  const { data: policies } = useEscalationPolicies(org);
  const updateSettings = useUpdateOrgSettings(org);
  const updateSessionSettings = useUpdateOrgSettings(org);
  const updateEscalationSettings = useUpdateOrgSettings(org);

  const [defaultPolicyUid, setDefaultPolicyUid] = useState<string>(NO_DEFAULT);
  const [escalationError, setEscalationError] = useState<string | null>(null);
  const [escalationSaved, setEscalationSaved] = useState(false);

  const [emailPattern, setEmailPattern] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [fieldError, setFieldError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [testEmail, setTestEmail] = useState("");

  // Hours, as a string so the field can be legitimately empty ("inherit the
  // platform default") rather than coerced to 0.
  const [sessionHours, setSessionHours] = useState("");
  const [sessionError, setSessionError] = useState<string | null>(null);
  const [sessionSaved, setSessionSaved] = useState(false);

  useEffect(() => {
    if (settings) {
      setEmailPattern(settings.registrationEmailPattern || "");
      setSessionHours(
        settings.sessionMaxDurationSeconds != null
          ? String(Math.round(settings.sessionMaxDurationSeconds / 3600))
          : "",
      );
      setDefaultPolicyUid(settings.defaultEscalationPolicyUid || NO_DEFAULT);
    }
  }, [settings]);

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setFieldError(null);
    setSaved(false);

    try {
      await updateSettings.mutateAsync({
        registrationEmailPattern: emailPattern,
      });
      setSaved(true);
      setTimeout(() => setSaved(false), 3000);
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.code === "INVALID_AUTO_JOIN_REGEX") {
          setFieldError(err.message);
        } else {
          setError(err.message);
        }
      } else {
        setError(t("settings.unexpectedError"));
      }
    }
  };

  const handleSaveSessionDuration = async (e: React.FormEvent) => {
    e.preventDefault();
    setSessionError(null);
    setSessionSaved(false);

    const trimmed = sessionHours.trim();
    const hours = trimmed === "" ? 0 : Number(trimmed);

    if (trimmed !== "" && (!Number.isFinite(hours) || hours < 0)) {
      setSessionError(t("settings.unexpectedError"));
      return;
    }

    try {
      await updateSessionSettings.mutateAsync({
        // A value <= 0 clears the override (see UpdateOrgSettingsRequest) —
        // an empty field means "inherit the platform default".
        sessionMaxDurationSeconds: Math.round(hours * 3600),
      });
      setSessionSaved(true);
      setTimeout(() => setSessionSaved(false), 3000);
    } catch (err) {
      setSessionError(
        err instanceof ApiError ? err.message : t("settings.unexpectedError"),
      );
    }
  };

  const handleSaveDefaultEscalation = async (e: React.FormEvent) => {
    e.preventDefault();
    setEscalationError(null);
    setEscalationSaved(false);

    try {
      await updateEscalationSettings.mutateAsync({
        // Empty string clears the org default; a UID sets it.
        defaultEscalationPolicyUid:
          defaultPolicyUid === NO_DEFAULT ? "" : defaultPolicyUid,
      });
      setEscalationSaved(true);
      setTimeout(() => setEscalationSaved(false), 3000);
    } catch (err) {
      setEscalationError(
        err instanceof ApiError ? err.message : t("settings.unexpectedError"),
      );
    }
  };

  const handleDeleteOrg = async () => {
    try {
      // Tombstone the slug BEFORE the request settles: list queries and the
      // live-events socket are still addressed to this org and start failing
      // the instant it is gone. Without this, redirectToExpiredLogin would
      // hard-navigate to the deleted org's login page (which 404s) and beat the
      // deliberate navigation below.
      markOrgDeleted(org);

      const session = await deleteOrg.mutateAsync(org);
      toast.success(t("settings.dangerZone.deleted", { org }));

      // Deleting an org must not sign the owner out (issue #206). The server
      // hands back a replacement session — scoped to a surviving org, or
      // org-less — and adopting it is what keeps the user authenticated.
      const nextOrg = adoptOrgDeletionSession(session);

      if (nextOrg) {
        await navigate({ to: "/orgs/$org", params: { org: nextOrg } });
      } else {
        await navigate({ to: "/no-org", search: { membershipPending: undefined } });
      }

      // Only now, off the dead org's routes, drop its cached queries. Every
      // org-scoped key carries the slug, so this evicts exactly that org and
      // leaves the surviving one's cache (already being refetched) alone.
      queryClient.removeQueries({
        predicate: (query) => query.queryKey.includes(org),
      });
    } catch (err) {
      toast.error(
        err instanceof ApiError ? err.message : t("settings.unexpectedError"),
      );
    }
  };

  let testResult: "match" | "no-match" | null = null;
  if (emailPattern && testEmail) {
    try {
      testResult = new RegExp(emailPattern).test(testEmail)
        ? "match"
        : "no-match";
    } catch {
      testResult = "no-match";
    }
  }

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-12">
        <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
      </div>
    );
  }

  return (
    <>
      {/* Owner-only, like the danger zone below — the server enforces it with
          RequireOrgOwner, so hiding the card from an admin is UX only. */}
      {user?.isOwner && <OrgProfileCard org={org} />}

      <Card className={user?.isOwner ? "mt-6" : undefined}>
        <CardHeader>
          <CardTitle>{t("settings.autoJoin")}</CardTitle>
          <CardDescription>
            {t("settings.autoJoinFullDescription")}
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
                <AlertDescription>{t("settings.saved")}</AlertDescription>
              </Alert>
            )}

            <div className="space-y-2">
              <Label htmlFor="emailPattern">{t("settings.emailPattern")}</Label>
              <Input
                id="emailPattern"
                type="text"
                placeholder={t("settings.emailPlaceholder")}
                value={emailPattern}
                onChange={(e) => {
                  setEmailPattern(e.target.value);
                  setFieldError(null);
                }}
                disabled={updateSettings.isPending}
                aria-invalid={fieldError != null}
              />
              {fieldError && (
                <p className="text-xs text-destructive">{fieldError}</p>
              )}
              <p
                className="text-xs text-muted-foreground"
                dangerouslySetInnerHTML={{ __html: t("settings.autoJoinHelp") }}
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="testEmail">
                {t("settings.testAgainstEmail", "Test against email")}
              </Label>
              <Input
                id="testEmail"
                type="email"
                value={testEmail}
                onChange={(e) => setTestEmail(e.target.value)}
                placeholder="user@example.com"
              />
              {testResult && (
                <p
                  className={`text-xs ${
                    testResult === "match"
                      ? "text-green-600"
                      : "text-muted-foreground"
                  }`}
                >
                  {testResult === "match"
                    ? t("settings.testMatch", "✓ matches the pattern")
                    : t("settings.testNoMatch", "✗ does not match the pattern")}
                </p>
              )}
            </div>

            <Button
              type="submit"
              disabled={updateSettings.isPending}
              data-testid="auto-join-pattern-save"
            >
              {updateSettings.isPending ? (
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

      <Card className="mt-6">
        <CardHeader>
          <CardTitle>{t("settings.sessionDuration")}</CardTitle>
          <CardDescription>
            {t("settings.sessionDurationFullDescription")}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSaveSessionDuration} className="space-y-4">
            {sessionError && (
              <Alert variant="destructive">
                <AlertCircle className="h-4 w-4" />
                <AlertDescription>{sessionError}</AlertDescription>
              </Alert>
            )}

            {sessionSaved && (
              <Alert>
                <Check className="h-4 w-4" />
                <AlertDescription>{t("settings.saved")}</AlertDescription>
              </Alert>
            )}

            <div className="space-y-2">
              <Label htmlFor="sessionHours">
                {t("settings.sessionDurationHoursLabel")}
              </Label>
              <Input
                id="sessionHours"
                type="number"
                min="0"
                step="1"
                inputMode="numeric"
                placeholder={t("settings.sessionDurationPlaceholder")}
                value={sessionHours}
                onChange={(e) => {
                  setSessionHours(e.target.value);
                  setSessionError(null);
                }}
                disabled={updateSessionSettings.isPending}
                aria-invalid={sessionError != null}
                className="max-w-xs"
                data-testid="session-duration-hours"
              />
              <p className="text-xs text-muted-foreground">
                {t("settings.sessionDurationHelp")}
              </p>
            </div>

            <Button
              type="submit"
              disabled={updateSessionSettings.isPending}
              data-testid="session-duration-save"
            >
              {updateSessionSettings.isPending ? (
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

      <Card className="mt-6">
        <CardHeader>
          <CardTitle>
            {t("settings.defaultEscalation", "Default escalation policy")}
          </CardTitle>
          <CardDescription>
            {t(
              "settings.defaultEscalationDescription",
              "Applied to checks that resolve to no policy of their own (check → group → org default → none). Leave unset for no org-wide fallback.",
            )}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSaveDefaultEscalation} className="space-y-4">
            {escalationError && (
              <Alert variant="destructive">
                <AlertCircle className="h-4 w-4" />
                <AlertDescription>{escalationError}</AlertDescription>
              </Alert>
            )}

            {escalationSaved && (
              <Alert>
                <Check className="h-4 w-4" />
                <AlertDescription>{t("settings.saved")}</AlertDescription>
              </Alert>
            )}

            <div className="space-y-2">
              <Label htmlFor="default-escalation-policy">
                {t("settings.defaultEscalation", "Default escalation policy")}
              </Label>
              <Select
                value={defaultPolicyUid}
                onValueChange={setDefaultPolicyUid}
              >
                <SelectTrigger
                  id="default-escalation-policy"
                  className="max-w-md"
                  data-testid="default-escalation-select"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NO_DEFAULT}>
                    {t("settings.defaultEscalationNone", "None (no fallback)")}
                  </SelectItem>
                  {(policies ?? []).map((p) => (
                    <SelectItem key={p.uid} value={p.uid}>
                      {p.name}
                      {(p.stepCount ?? p.steps?.length ?? 0) === 0
                        ? " — silent"
                        : ""}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {settings?.inheritingCheckCount != null &&
                defaultPolicyUid !== NO_DEFAULT && (
                  <p
                    className="text-xs text-muted-foreground"
                    data-testid="escalation-blast-radius"
                  >
                    {settings.inheritingCheckCount}{" "}
                    {settings.inheritingCheckCount === 1 ? "check" : "checks"}{" "}
                    currently inherit and will start using this policy.
                  </p>
                )}
            </div>

            <Button
              type="submit"
              disabled={updateEscalationSettings.isPending}
              data-testid="default-escalation-save"
            >
              {updateEscalationSettings.isPending ? (
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

      {/* Owner-only. The server enforces it too (RequireOrgOwner): an admin who
          reaches the endpoint gets a 403, the hidden card is only UX. */}
      {user?.isOwner && (
        <DangerZone
          title={t("settings.dangerZone.title")}
          description={t("settings.dangerZone.description")}
          testId="org-danger-zone"
        >
          <div className="space-y-3">
            <p className="text-sm text-muted-foreground">
              {t("settings.dangerZone.deleteHelp", { org })}
            </p>
            <ConfirmByTypingButton
              buttonLabel={t("settings.dangerZone.deleteButton")}
              title={t("settings.dangerZone.confirmTitle")}
              description={t("settings.dangerZone.confirmBody", { org })}
              inputLabel={t("settings.dangerZone.confirmInputLabel", { org })}
              confirmValue={org}
              confirmLabel={t("settings.dangerZone.confirmAction")}
              onConfirm={handleDeleteOrg}
              disabled={deleteOrg.isPending}
              testId="delete-org"
            />
          </div>
        </DangerZone>
      )}
    </>
  );
}
