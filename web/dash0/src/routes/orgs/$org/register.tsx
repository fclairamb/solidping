import { useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { Trans, useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Activity, AlertCircle, CheckCircle2, Loader2 } from "lucide-react";
import { ApiError } from "@/api/client";
import { useRegister } from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/register")({
  component: RegisterPage,
});

function RegisterPage() {
  const { t } = useTranslation(["auth", "common"]);
  const { org } = Route.useParams();
  const register = useRegister();

  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState(false);

  const handleSubmit = async (e: React.FormEvent) => {
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
      await register.mutateAsync({
        name: name || undefined,
        email,
        password,
      });
      setSuccess(true);
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message);
      } else {
        setError(t("auth:unexpectedError"));
      }
    }
  };

  if (success) {
    return (
      <div className="min-h-screen flex items-center justify-center bg-background p-4">
        <Card className="w-full max-w-md">
          <CardHeader className="text-center">
            <div className="flex justify-center mb-4">
              <CheckCircle2 className="h-12 w-12 text-green-500" />
            </div>
            <CardTitle className="text-2xl">{t("auth:checkYourEmail")}</CardTitle>
          </CardHeader>
          <CardContent className="text-center">
            <p className="text-muted-foreground mb-4">
              <Trans
                i18nKey="auth:confirmationLinkSentTo"
                values={{ email }}
                components={{ strong: <strong /> }}
              />
            </p>
            <Link
              to="/orgs/$org/login"
              params={{ org }}
              search={{ session_expired: false, returnTo: undefined }}
              className="text-primary underline-offset-4 hover:underline text-sm"
            >
              {t("auth:backToLogin")}
            </Link>
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
          <CardTitle className="text-2xl">{t("auth:createAccount")}</CardTitle>
          <p className="text-sm text-muted-foreground mt-1">
            {t("auth:signUpForSolidPing")}
          </p>
        </CardHeader>
        <CardContent>
          {error && (
            <Alert variant="destructive" className="mb-4">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}

          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="name">{t("auth:nameOptional")}</Label>
              <Input
                id="name"
                type="text"
                placeholder={t("auth:yourNamePlaceholder")}
                value={name}
                onChange={(e) => setName(e.target.value)}
                disabled={register.isPending}
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
                disabled={register.isPending}
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
                disabled={register.isPending}
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
                disabled={register.isPending}
              />
            </div>

            <Button
              type="submit"
              className="w-full"
              disabled={register.isPending}
            >
              {register.isPending ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  {t("auth:creatingAccount")}
                </>
              ) : (
                t("auth:createAccountButton")
              )}
            </Button>
          </form>

          <div className="mt-4 text-center text-sm text-muted-foreground">
            {t("auth:alreadyHaveAccount")}{" "}
            <Link
              to="/orgs/$org/login"
              params={{ org }}
              search={{ session_expired: false, returnTo: undefined }}
              className="text-primary underline-offset-4 hover:underline"
            >
              {t("auth:signIn")}
            </Link>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
