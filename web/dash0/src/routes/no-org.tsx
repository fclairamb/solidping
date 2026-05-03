import { useState } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Activity, AlertCircle, Loader2, X } from "lucide-react";
import { ApiError, setToken } from "@/api/client";
import {
  useCreateOrg,
  useCreateMembershipRequest,
  useCancelMembershipRequest,
  useMyMembershipRequests,
} from "@/api/hooks";
import { useAuth } from "@/contexts/AuthContext";

export const Route = createFileRoute("/no-org")({
  component: NoOrgPage,
});

function slugify(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 20);
}

function NoOrgPage() {
  const { t } = useTranslation("auth");
  const navigate = useNavigate();
  const { logout } = useAuth();

  return (
    <div className="min-h-screen bg-background p-4 sm:p-8 flex flex-col items-center">
      <div className="w-full max-w-4xl space-y-6">
        <div className="text-center">
          <div className="flex justify-center mb-3">
            <Activity className="h-12 w-12 text-primary" />
          </div>
          <h1 className="text-2xl font-semibold">{t("noOrg.welcome")}</h1>
          <p className="text-sm text-muted-foreground mt-1">
            {t("noOrg.subtitle")}
          </p>
        </div>

        <div className="grid gap-4 md:grid-cols-2">
          <CreateOrgCard />
          <JoinOrgCard />
        </div>

        <PendingRequestsList />

        <div className="text-center">
          <Button
            variant="ghost"
            size="sm"
            onClick={async () => {
              await logout();
              navigate({
                to: "/orgs/$org/login",
                params: { org: "default" },
                search: { session_expired: false, returnTo: undefined },
              });
            }}
          >
            {t("noOrg.signOut")}
          </Button>
        </div>
      </div>
    </div>
  );
}

function CreateOrgCard() {
  const { t } = useTranslation("auth");
  const navigate = useNavigate();
  const createOrg = useCreateOrg();

  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugTouched, setSlugTouched] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleNameChange = (value: string) => {
    setName(value);
    if (!slugTouched) {
      setSlug(slugify(value));
    }
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    try {
      const result = await createOrg.mutateAsync({ name, slug });
      if (result.accessToken) setToken(result.accessToken);
      navigate({ to: "/orgs/$org", params: { org: result.slug } });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t("unexpectedError"));
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-lg">{t("noOrg.createTitle")}</CardTitle>
        <p className="text-sm text-muted-foreground">
          {t("noOrg.createDescription")}
        </p>
      </CardHeader>
      <CardContent>
        {error && (
          <Alert variant="destructive" className="mb-4">
            <AlertCircle className="h-4 w-4" />
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}

        <form onSubmit={handleSubmit} className="space-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="orgName">{t("noOrg.orgName")}</Label>
            <Input
              id="orgName"
              value={name}
              onChange={(e) => handleNameChange(e.target.value)}
              required
              disabled={createOrg.isPending}
              placeholder={t("noOrg.orgNamePlaceholder")}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="orgSlug">{t("noOrg.slug")}</Label>
            <Input
              id="orgSlug"
              value={slug}
              onChange={(e) => {
                setSlug(e.target.value);
                setSlugTouched(true);
              }}
              required
              pattern="[a-z0-9][a-z0-9-]{1,18}[a-z0-9]"
              title={t("noOrg.slugTitle")}
              disabled={createOrg.isPending}
              placeholder={t("noOrg.slugPlaceholder")}
            />
          </div>
          <Button type="submit" className="w-full" disabled={createOrg.isPending}>
            {createOrg.isPending ? (
              <>
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                {t("noOrg.creating")}
              </>
            ) : (
              t("noOrg.createOrg")
            )}
          </Button>
        </form>
      </CardContent>
    </Card>
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

  if (success) {
    return (
      <Card>
        <CardHeader>
          <CardTitle className="text-lg">{t("noOrg.joinTitle")}</CardTitle>
        </CardHeader>
        <CardContent>
          <Alert>
            <AlertDescription>{t("noOrg.joinSent")}</AlertDescription>
          </Alert>
        </CardContent>
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-lg">{t("noOrg.joinTitle")}</CardTitle>
        <p className="text-sm text-muted-foreground">
          {t("noOrg.joinDescription")}
        </p>
      </CardHeader>
      <CardContent>
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
      </CardContent>
    </Card>
  );
}

function PendingRequestsList() {
  const { t } = useTranslation("auth");
  const { data, isLoading } = useMyMembershipRequests();
  const cancel = useCancelMembershipRequest();

  if (isLoading || !data || data.data.length === 0) return null;

  const visible = data.data.filter(
    (r) => r.status === "pending" || r.status === "rejected"
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
