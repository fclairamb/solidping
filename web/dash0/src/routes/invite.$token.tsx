import { useState } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { PasswordInput } from "@/components/ui/password-input";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Logo } from "@/components/ui/logo";
import { AlertCircle, AlertTriangle, Loader2, Mail, RotateCw } from "lucide-react";
import { ApiError, getToken } from "@/api/client";
import { useInviteInfo, useAcceptInvite } from "@/api/hooks";
import { useAuth } from "@/contexts/AuthContext";
import { isInviteInvalidError } from "@/lib/invite-error";

export const Route = createFileRoute("/invite/$token")({
  component: AcceptInvitePage,
});

function AcceptInvitePage() {
  const { t } = useTranslation(["auth", "common"]);
  const { token } = Route.useParams();
  const navigate = useNavigate();
  const {
    data: inviteInfo,
    isLoading: infoLoading,
    error: infoError,
    isRefetching: infoRefetching,
    refetch: refetchInviteInfo,
  } = useInviteInfo(token);
  const acceptInvite = useAcceptInvite();
  const { acceptInviteSession } = useAuth();

  const isAuthenticated = !!getToken();

  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);

  const handleAcceptAuthenticated = async () => {
    setError(null);
    try {
      const result = await acceptInvite.mutateAsync({ token });
      acceptInviteSession(result);
      navigate({
        to: "/orgs/$org",
        params: { org: result.organization.slug },
      });
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message);
      } else {
        setError(t("auth:unexpectedError"));
      }
    }
  };

  const handleAcceptNewUser = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);

    if (password.length < 8) {
      setError(t("auth:passwordTooShort"));
      return;
    }

    try {
      const result = await acceptInvite.mutateAsync({
        token,
        name: name || undefined,
        password,
      });
      acceptInviteSession(result);
      navigate({
        to: "/orgs/$org",
        params: { org: result.organization.slug },
      });
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message);
      } else {
        setError(t("auth:unexpectedError"));
      }
    }
  };

  if (infoLoading) {
    return (
      <div className="min-h-screen flex items-center justify-center bg-background p-4">
        <Card className="w-full max-w-md border-t-4 border-t-brand">
          <CardContent className="flex items-center justify-center py-12">
            <Loader2 className="h-8 w-8 animate-spin text-primary" />
          </CardContent>
        </Card>
      </div>
    );
  }

  // Only a genuinely dead token (404 NOT_FOUND / 410 EXPIRED) means "invalid
  // or expired". A 429, a 5xx, or a network failure is transient — telling
  // someone their perfectly valid invitation has expired when the server
  // merely rate-limited them is a dead end they cannot recover from, so those
  // get a distinct, retryable state instead.
  if (infoError && !isInviteInvalidError(infoError)) {
    return (
      <div className="min-h-screen flex items-center justify-center bg-background p-4">
        <Card className="w-full max-w-md border-t-4 border-t-brand">
          <CardHeader className="text-center">
            <div className="flex justify-center mb-4">
              <AlertTriangle className="h-12 w-12 text-destructive" />
            </div>
            <CardTitle className="text-2xl">{t("auth:invite.temporaryError")}</CardTitle>
          </CardHeader>
          <CardContent className="text-center space-y-4">
            <p className="text-muted-foreground">
              {t("auth:invite.temporaryErrorDescription")}
            </p>
            <Button onClick={() => void refetchInviteInfo()} disabled={infoRefetching}>
              {infoRefetching ? (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              ) : (
                <RotateCw className="mr-2 h-4 w-4" />
              )}
              {t("common:retry")}
            </Button>
          </CardContent>
        </Card>
      </div>
    );
  }

  if (infoError || !inviteInfo) {
    return (
      <div className="min-h-screen flex items-center justify-center bg-background p-4">
        <Card className="w-full max-w-md border-t-4 border-t-brand">
          <CardHeader className="text-center">
            <div className="flex justify-center mb-4">
              <AlertCircle className="h-12 w-12 text-destructive" />
            </div>
            <CardTitle className="text-2xl">{t("auth:invite.invalid")}</CardTitle>
          </CardHeader>
          <CardContent className="text-center">
            <p className="text-muted-foreground">
              {t("auth:invite.invalidDescription")}
            </p>
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-background p-4">
      <Card className="w-full max-w-md border-t-4 border-t-brand">
        <CardHeader className="text-center">
          <div className="flex justify-center mb-4">
            <Logo size={64} />
          </div>
          <CardTitle className="text-2xl">
            {t("auth:invite.joinTitle", { orgName: inviteInfo.orgName })}
          </CardTitle>
          <p className="text-sm text-muted-foreground mt-1">
            {t("auth:invite.invitedAs")}{" "}
            <span className="font-medium">{inviteInfo.role}</span>
          </p>
          {isAuthenticated ? (
            <p className="mt-3 text-sm text-muted-foreground">
              {t("auth:invite.alreadySignedIn")}
            </p>
          ) : inviteInfo.email ? (
            <div className="mt-3 flex items-center justify-center gap-2 text-sm">
              <Mail className="h-4 w-4 shrink-0 text-muted-foreground" />
              <span className="break-all" title={inviteInfo.email}>
                {t("auth:invite.creatingAccountFor", {
                  email: inviteInfo.email,
                })}
              </span>
            </div>
          ) : null}
        </CardHeader>
        <CardContent>
          {error && (
            <Alert variant="destructive" className="mb-4">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}

          {isAuthenticated ? (
            <Button
              className="w-full"
              onClick={handleAcceptAuthenticated}
              disabled={acceptInvite.isPending}
            >
              {acceptInvite.isPending ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  {t("auth:invite.joining")}
                </>
              ) : (
                t("auth:acceptInvitation")
              )}
            </Button>
          ) : (
            <form onSubmit={handleAcceptNewUser} className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="name">{t("auth:nameOptional")}</Label>
                <Input
                  id="name"
                  type="text"
                  placeholder={t("auth:yourNamePlaceholder")}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  disabled={acceptInvite.isPending}
                />
              </div>

              <div className="space-y-2">
                <Label htmlFor="password">{t("common:password")}</Label>
                <PasswordInput
                  id="password"
                  autoComplete="new-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  required
                  disabled={acceptInvite.isPending}
                />
              </div>

              <Button
                type="submit"
                className="w-full"
                disabled={acceptInvite.isPending}
              >
                {acceptInvite.isPending ? (
                  <>
                    <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                    {t("auth:invite.joining")}
                  </>
                ) : (
                  t("auth:invite.createAndJoin")
                )}
              </Button>
            </form>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
