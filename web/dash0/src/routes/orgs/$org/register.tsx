import { useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { Trans, useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { PasswordInput } from "@/components/ui/password-input";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { AlertCircle, CheckCircle2, Loader2 } from "lucide-react";

import { Logo } from "@/components/ui/logo";
import { AuthSplitLayout } from "@/components/layout/auth-split-layout";
import { ApiError } from "@/api/client";
import { useProviders, useRegister } from "@/api/hooks";
import { OAuthProviderButtons } from "@/components/auth/oauth-provider-buttons";
import { clearSignupAttribution, readSignupAttribution } from "@/lib/attribution";

export const Route = createFileRoute("/orgs/$org/register")({
  component: RegisterPage,
});

function RegisterPage() {
  const { t } = useTranslation(["auth", "common"]);
  const { org } = Route.useParams();
  const register = useRegister();
  const { data: providersData } = useProviders();

  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState(false);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);

    if (password.length < 8) {
      setError(t("auth:passwordTooShort"));
      return;
    }

    try {
      await register.mutateAsync({
        name: name || undefined,
        email,
        password,
        // Where this signup came from, if a tagged link brought the visitor
        // here. The server keeps it on the pending registration and stores
        // it on the account at confirmation (spec 2026-09-07-03).
        attribution: readSignupAttribution(),
      });
      // Handed over; a second account from this tab must not inherit it.
      clearSignupAttribution();
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
      <AuthSplitLayout>
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
      </AuthSplitLayout>
    );
  }

  return (
    <AuthSplitLayout>
      <Card className="w-full max-w-md border-t-4 border-t-brand">
        <CardHeader className="text-center">
          <div className="flex justify-center mb-4">
            <Logo size={64} />
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

          {/* Every provider callback runs findOrCreateUser, so a first-time
              "Continue with GitHub" creates the account — these buttons are a
              sign-up path, not a sign-in one wrongly pasted here (spec
              2026-09-09-02). Three deliberate differences from /login:

              - No promoted "last used" slot: a visitor on /register is
                claiming to be new, so promoting a remembered provider is the
                wrong signal. The grid still records oauth:<type> on click, so
                the NEXT login promotes whatever they signed up with.
              - No passkey button: /passkeys/register/begin|finish are mounted
                on rootAuthProtected, so a passkey can do nothing for someone
                without an account yet. Passkeys are added from account
                settings after the first login.
              - Not gated on registrationEnabled: that flag mirrors
                auth.registration_email_pattern and gates *password*
                self-registration only (handlers/auth/service.go);
                findOrCreateUser never consults it, and /login already shows
                the same buttons regardless. This page follows the backend,
                not the password form. */}
          <OAuthProviderButtons
            org={org}
            providers={providersData?.providers}
            disabled={register.isPending}
            testIdPrefix="register"
          />

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
              <PasswordInput
                id="password"
                autoComplete="new-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
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
    </AuthSplitLayout>
  );
}
