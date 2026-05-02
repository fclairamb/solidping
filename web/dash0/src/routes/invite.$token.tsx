import { useState } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Activity,
  AlertCircle,
  Loader2,
  Mail,
} from "lucide-react";
import { ApiError, getToken, setToken } from "@/api/client";
import { useInviteInfo, useAcceptInvite } from "@/api/hooks";

export const Route = createFileRoute("/invite/$token")({
  component: AcceptInvitePage,
});

function AcceptInvitePage() {
  const { t } = useTranslation(["auth", "common"]);
  const { token } = Route.useParams();
  const navigate = useNavigate();
  const { data: inviteInfo, isLoading: infoLoading, error: infoError } = useInviteInfo(token);
  const acceptInvite = useAcceptInvite();

  const isAuthenticated = !!getToken();

  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [error, setError] = useState<string | null>(null);

  const handleAcceptAuthenticated = async () => {
    setError(null);
    try {
      const result = await acceptInvite.mutateAsync({ token });
      setToken(result.accessToken);
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

    if (password !== confirmPassword) {
      setError(t("auth:passwordsDoNotMatch"));
      return;
    }

    if (password.length < 8) {
      setError(t("auth:passwordTooShort"));
      return;
    }

    try {
      const result = await acceptInvite.mutateAsync({
        token,
        name: name || undefined,
        email,
        password,
      });
      setToken(result.accessToken);
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
        <Card className="w-full max-w-md">
          <CardContent className="flex items-center justify-center py-12">
            <Loader2 className="h-8 w-8 animate-spin text-primary" />
          </CardContent>
        </Card>
      </div>
    );
  }

  if (infoError || !inviteInfo) {
    return (
      <div className="min-h-screen flex items-center justify-center bg-background p-4">
        <Card className="w-full max-w-md">
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
      <Card className="w-full max-w-md">
        <CardHeader className="text-center">
          <div className="flex justify-center mb-4">
            <Activity className="h-12 w-12 text-primary" />
          </div>
          <CardTitle className="text-2xl">
            {t("auth:invite.joinTitle", { orgName: inviteInfo.orgName })}
          </CardTitle>
          <p className="text-sm text-muted-foreground mt-1">
            {t("auth:invite.invitedAs")}{" "}
            <span className="font-medium">{inviteInfo.role}</span>
          </p>
          {inviteInfo.email && (
            <div className="flex items-center justify-center gap-1 mt-2 text-sm text-muted-foreground">
              <Mail className="h-3 w-3" />
              <span>{inviteInfo.email}</span>
            </div>
          )}
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
                <Label htmlFor="email">{t("common:email")}</Label>
                <Input
                  id="email"
                  type="email"
                  placeholder={t("auth:emailPlaceholder")}
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  required
                  disabled={acceptInvite.isPending}
                />
              </div>

              <div className="space-y-2">
                <Label htmlFor="password">{t("common:password")}</Label>
                <Input
                  id="password"
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  required
                  disabled={acceptInvite.isPending}
                />
              </div>

              <div className="space-y-2">
                <Label htmlFor="confirmPassword">{t("auth:confirmPassword")}</Label>
                <Input
                  id="confirmPassword"
                  type="password"
                  value={confirmPassword}
                  onChange={(e) => setConfirmPassword(e.target.value)}
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
