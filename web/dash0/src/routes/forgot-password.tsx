import { useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Activity, AlertCircle, CheckCircle2, Loader2 } from "lucide-react";
import { ApiError } from "@/api/client";
import { useRequestPasswordReset } from "@/api/hooks";

export const Route = createFileRoute("/forgot-password")({
  component: ForgotPasswordPage,
});

function ForgotPasswordPage() {
  const { t } = useTranslation("auth");
  const [email, setEmail] = useState("");
  const [submitted, setSubmitted] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const requestReset = useRequestPasswordReset();

  // Anti-enumeration is the *server's* job — it returns 200 for both valid
  // and invalid emails. Real network / 429 / 500 failures must surface so
  // the user knows to retry; swallowing them as success is misleading.
  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    try {
      await requestReset.mutateAsync({ email });
      setSubmitted(true);
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message || t("resetSendFailed"));
      } else {
        setError(t("resetSendFailed"));
      }
    }
  };

  return (
    <div className="min-h-screen flex items-center justify-center bg-background p-4">
      <Card className="w-full max-w-md">
        <CardHeader className="text-center">
          <div className="flex justify-center mb-4">
            {submitted ? (
              <CheckCircle2 className="h-12 w-12 text-green-500" />
            ) : (
              <Activity className="h-12 w-12 text-primary" />
            )}
          </div>
          <CardTitle className="text-2xl">
            {submitted ? t("checkYourEmail") : t("resetYourPassword")}
          </CardTitle>
        </CardHeader>
        <CardContent>
          {submitted ? (
            <div className="text-center space-y-4">
              <p className="text-muted-foreground">{t("resetLinkSent")}</p>
              <Link
                to="/login"
                className="text-primary underline-offset-4 hover:underline text-sm"
              >
                {t("backToLogin")}
              </Link>
            </div>
          ) : (
            <form onSubmit={handleSubmit} className="space-y-4">
              {error && (
                <Alert variant="destructive">
                  <AlertCircle className="h-4 w-4" />
                  <AlertDescription>{error}</AlertDescription>
                </Alert>
              )}
              <div className="space-y-2">
                <Label htmlFor="email">{t("email", { ns: "common" })}</Label>
                <Input
                  id="email"
                  type="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  required
                  disabled={requestReset.isPending}
                />
              </div>
              <Button
                type="submit"
                className="w-full"
                disabled={requestReset.isPending}
              >
                {requestReset.isPending ? (
                  <>
                    <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                    {t("sendingResetLink")}
                  </>
                ) : (
                  t("sendResetLink")
                )}
              </Button>
              <div className="text-center">
                <Link
                  to="/login"
                  className="text-sm text-muted-foreground hover:underline"
                >
                  {t("backToLogin")}
                </Link>
              </div>
            </form>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
