import { useMemo, useState } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Logo } from "@/components/ui/logo";
import { AuroraPanel } from "@/components/ui/aurora-panel";
import { AlertCircle, Loader2, X } from "lucide-react";
import { ApiError } from "@/api/client";
import {
  useCreateMembershipRequest,
  useCancelMembershipRequest,
  useMyMembershipRequests,
} from "@/api/hooks";
import { useAuth } from "@/contexts/AuthContext";
import { CreateOrgCard } from "@/components/shared/create-org-card";
import { SecondaryDisclosureCard } from "@/components/ui/secondary-disclosure-card";
import { randomOrgNameSeed, suggestOrgName } from "@/lib/org-name-suggestion";

export const Route = createFileRoute("/no-org")({
  // `membershipPending` is set by the backend's federated-login callbacks
  // (see server/internal/handlers/auth/join_policy.go) when a social/SSO
  // sign-in authenticated the user but the target org did not admit them:
  // no membership was created, a join request is awaiting an admin, and the
  // session handed over is org-less. It names that org so we can explain why
  // the user landed here instead of on its dashboard.
  validateSearch: (
    search: Record<string, unknown>,
  ): { membershipPending?: string } => ({
    membershipPending:
      typeof search.membershipPending === "string" && search.membershipPending
        ? search.membershipPending
        : undefined,
  }),
  component: NoOrgPage,
});

function NoOrgPage() {
  const { t } = useTranslation("auth");
  const navigate = useNavigate();
  const { logout, user } = useAuth();
  const { membershipPending } = Route.useSearch();

  // "Here is an organization for you — click Create" is the whole point of this
  // screen for a brand-new account (spec 2026-09-05-01): the possessive form
  // when we know a first name, a friendly two-word name otherwise.
  //
  // The randomness is drawn ONCE into state; the proposal itself is then a pure
  // function of (name, seed, locale). Rolling the dice inside the memo would
  // reshuffle the name whenever the translator's identity changed — i.e. rewrite
  // the field under the user's cursor.
  const [nameSeed] = useState(randomOrgNameSeed);
  const suggestedName = useMemo(() => {
    const suggestion = suggestOrgName(user?.name, nameSeed);

    return suggestion.kind === "personal"
      ? t("createOrg.suggestedPersonal", { firstName: suggestion.firstName })
      : suggestion.name;
  }, [nameSeed, t, user?.name]);

  return (
    <div className="min-h-screen bg-background p-4 sm:p-8 flex flex-col items-center">
      <div className="w-full max-w-4xl space-y-6">
        <AuroraPanel className="rounded-3xl p-8 sm:p-10">
          <div className="flex flex-col items-center gap-3 text-center">
            <Logo size={56} />
            <h1 className="text-2xl font-semibold tracking-tight">
              {t("noOrg.welcome")}
            </h1>
            <p className="max-w-prose text-sm text-white/70">
              {t("noOrg.subtitle")}
            </p>
          </div>
        </AuroraPanel>

        {membershipPending && (
          <Alert data-testid="membership-pending-alert">
            <AlertCircle className="h-4 w-4" />
            <AlertDescription>
              {t("noOrg.membershipPending", { org: membershipPending })}
            </AlertDescription>
          </Alert>
        )}

        {/* Create first and full width: it is the action we expect. Joining an
            existing org stays available below, visibly secondary — invited
            colleagues still need it. */}
        <CreateOrgCard suggestedName={suggestedName} />
        <JoinOrgCard />

        <PendingRequestsList />

        <div className="text-center">
          <Button
            variant="ghost"
            size="sm"
            onClick={async () => {
              await logout();
              // /login owns the "which org's login page?" fallback (last
              // visited, else the platform default) — hardcoding "default"
              // here dead-ended on installs that have no such org.
              navigate({ to: "/login", search: { returnTo: undefined } });
            }}
          >
            {t("noOrg.signOut")}
          </Button>
        </div>
      </div>
    </div>
  );
}

function JoinOrgCard() {
  const { t } = useTranslation("auth");
  const createRequest = useCreateMembershipRequest();

  const [orgSlug, setOrgSlug] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState(false);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    try {
      await createRequest.mutateAsync({
        orgSlug,
        message: message || undefined,
      });
      setSuccess(true);
      setOrgSlug("");
      setMessage("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t("unexpectedError"));
    }
  };

  return (
    <SecondaryDisclosureCard
      title={t("noOrg.joinTitle")}
      description={t("noOrg.joinDescription")}
      expandLabel={t("noOrg.joinExpand")}
      collapseLabel={t("noOrg.joinCollapse")}
      data-testid="no-org-join-toggle"
    >
      {success ? (
        <Alert>
          <AlertDescription>{t("noOrg.joinSent")}</AlertDescription>
        </Alert>
      ) : (
        <>
          {error && (
            <Alert variant="destructive" className="mb-4">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}

          <form onSubmit={handleSubmit} className="space-y-3">
            <div className="space-y-1.5">
              <Label htmlFor="joinOrgSlug">{t("noOrg.joinSlugLabel")}</Label>
              <Input
                id="joinOrgSlug"
                value={orgSlug}
                onChange={(e) => setOrgSlug(e.target.value)}
                required
                pattern="[a-z0-9][a-z0-9-]{1,18}[a-z0-9]"
                disabled={createRequest.isPending}
                placeholder={t("noOrg.joinSlugPlaceholder")}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="joinMessage">{t("noOrg.joinMessageLabel")}</Label>
              <Textarea
                id="joinMessage"
                value={message}
                onChange={(e) => setMessage(e.target.value)}
                disabled={createRequest.isPending}
                placeholder={t("noOrg.joinMessagePlaceholder")}
                rows={3}
              />
            </div>
            <Button
              type="submit"
              className="w-full"
              disabled={createRequest.isPending}
              variant="outline"
            >
              {createRequest.isPending ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  {t("noOrg.joining")}
                </>
              ) : (
                t("noOrg.requestJoin")
              )}
            </Button>
          </form>
        </>
      )}
    </SecondaryDisclosureCard>
  );
}

function PendingRequestsList() {
  const { t } = useTranslation("auth");
  const { data, isLoading } = useMyMembershipRequests();
  const cancel = useCancelMembershipRequest();

  if (isLoading || !data || data.data.length === 0) return null;

  const visible = data.data.filter(
    (r) => r.status === "pending" || r.status === "rejected",
  );

  if (visible.length === 0) return null;

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-lg">{t("noOrg.pendingTitle")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-2">
        {visible.map((req) => (
          <div
            key={req.uid}
            className="flex items-center justify-between rounded border p-3 text-sm"
          >
            <div className="min-w-0 flex-1">
              <div className="font-medium truncate">
                {req.organization.name || req.organization.slug}
              </div>
              <div className="text-muted-foreground text-xs">
                {req.status === "pending"
                  ? t("noOrg.statusPending")
                  : t("noOrg.statusRejected", {
                      reason: req.decisionReason || "",
                    })}
              </div>
            </div>
            {req.status === "pending" && (
              <Button
                variant="ghost"
                size="icon"
                onClick={() => cancel.mutate(req.uid)}
                disabled={cancel.isPending}
                aria-label={t("noOrg.cancelRequest")}
              >
                <X className="h-4 w-4" />
              </Button>
            )}
          </div>
        ))}
      </CardContent>
    </Card>
  );
}
