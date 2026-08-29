import { useEffect, useRef, useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { AlertCircle, CheckCircle2, Loader2 } from "lucide-react";
import { AuthSplitLayout } from "@/components/layout/auth-split-layout";
import { useConfirmRegistration } from "@/api/hooks";
import {
  applyConfirmRegistrationHandoff,
  ConfirmRegistrationSessionError,
} from "@/lib/confirm-registration-handoff";

export const Route = createFileRoute("/confirm-registration/$token")({
  component: ConfirmRegistrationPage,
});

function ConfirmRegistrationPage() {
  const { t } = useTranslation("auth");
  const { token } = Route.useParams();
  const navigate = useNavigate();
  const confirmRegistration = useConfirmRegistration();
  const [error, setError] = useState<string | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  // Set only for ConfirmRegistrationSessionError: the account WAS created,
  // just no session came back, so the error state should point the user to
  // log in rather than imply confirmation itself failed (spec
  // 2026-08-29-06).
  const [accountCreatedNoSession, setAccountCreatedNoSession] = useState(false);
  // Guards against StrictMode's dev double-invoke of this effect (mount →
  // cleanup → remount, synchronously, before `confirmRegistration.isPending`
  // has had a chance to flip): a `useState`-derived guard is not enough
  // because the second invocation reads the pre-mutation state, so it fires
  // the one-shot confirm-registration token twice and the retry lands on
  // "confirmation failed" even though the first call already succeeded. A
  // ref set synchronously on the first run — and persisted across the
  // remount, unlike state — closes that window. Keyed by token so a real
  // navigation to a different token still fires.
  const firedTokenRef = useRef<string | null>(null);

  useEffect(() => {
    if (!token || firedTokenRef.current === token) return;
    firedTokenRef.current = token;

    confirmRegistration
      .mutateAsync({ token })
      .then((data) => {
        const target = applyConfirmRegistrationHandoff(data);
        setConfirmed(true);
        setTimeout(() => {
          navigate(target);
        }, 1500);
      })
      .catch((err) => {
        if (err instanceof ConfirmRegistrationSessionError) {
          setAccountCreatedNoSession(true);
          setError(t("confirm.sessionFailedMessage"));
        } else {
          setError(err.message || t("confirm.failedMessage"));
        }
      });
  }, [token]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <AuthSplitLayout>
      <Card className="w-full max-w-md border-t-4 border-t-brand">
        <CardHeader className="text-center">
          {error ? (
            <>
              <div className="flex justify-center mb-4">
                {accountCreatedNoSession ? (
                  <CheckCircle2 className="h-12 w-12 text-status-warning" />
                ) : (
                  <AlertCircle className="h-12 w-12 text-destructive" />
                )}
              </div>
              <CardTitle className="text-2xl">
                {accountCreatedNoSession ? t("confirm.sessionFailed") : t("confirm.failed")}
              </CardTitle>
            </>
          ) : confirmed ? (
            <>
              <div className="flex justify-center mb-4">
                <CheckCircle2 className="h-12 w-12 text-green-500" />
              </div>
              <CardTitle className="text-2xl">{t("confirm.confirmed")}</CardTitle>
            </>
          ) : (
            <>
              <div className="flex justify-center mb-4">
                <Loader2 className="h-12 w-12 animate-spin text-primary" />
              </div>
              <CardTitle className="text-2xl">{t("confirm.confirming")}</CardTitle>
            </>
          )}
        </CardHeader>
        <CardContent className="text-center">
          {error && (
            <div className="space-y-4">
              <Alert variant={accountCreatedNoSession ? "warning" : "destructive"}>
                <AlertCircle className="h-4 w-4" />
                <AlertDescription>{error}</AlertDescription>
              </Alert>
              {accountCreatedNoSession && (
                <Link
                  to="/login"
                  search={{ returnTo: undefined }}
                  className="text-primary underline-offset-4 hover:underline text-sm"
                >
                  {t("signIn")}
                </Link>
              )}
            </div>
          )}
          {confirmed && (
            <p className="text-muted-foreground">{t("confirm.redirecting")}</p>
          )}
          {!error && !confirmed && (
            <p className="text-muted-foreground">{t("confirm.verifying")}</p>
          )}
        </CardContent>
      </Card>
    </AuthSplitLayout>
  );
}
