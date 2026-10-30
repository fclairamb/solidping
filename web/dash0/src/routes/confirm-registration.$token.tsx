import { useRef, useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Logo } from "@/components/ui/logo";
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
  // The token is one-shot, so it is spent only on a click, never on load:
  // mail link scanners open the link (some run its JavaScript) before the
  // human does, and used to burn the token, leaving the real visitor on
  // "confirmation failed".
  //
  // A ref, not state, guards the call: a double click lands both handlers
  // before `confirmRegistration.isPending` re-renders, and the second call
  // would spend nothing but report "confirmation failed" over the first
  // one's success. Keyed by token so a real navigation to a different token
  // can still confirm.
  const firedTokenRef = useRef<string | null>(null);

  const handleConfirm = () => {
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
  };

  const pending = confirmRegistration.isPending;
  const idle = !error && !confirmed && !pending;

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
              <CardTitle className="text-2xl" data-testid="confirm-registration-title">
                {accountCreatedNoSession ? t("confirm.sessionFailed") : t("confirm.failed")}
              </CardTitle>
            </>
          ) : confirmed ? (
            <>
              <div className="flex justify-center mb-4">
                <CheckCircle2 className="h-12 w-12 text-green-500" />
              </div>
              <CardTitle className="text-2xl" data-testid="confirm-registration-title">
                {t("confirm.confirmed")}
              </CardTitle>
            </>
          ) : pending ? (
            <>
              <div className="flex justify-center mb-4">
                <Loader2 className="h-12 w-12 animate-spin text-primary" />
              </div>
              <CardTitle className="text-2xl" data-testid="confirm-registration-title">
                {t("confirm.confirming")}
              </CardTitle>
            </>
          ) : (
            <>
              {/* Below lg the layout shows the wordmark above the card: the
                  card's own mark only appears on lg+, so a phone sees one. */}
              <div className="mb-4 hidden justify-center lg:flex">
                <Logo size={64} />
              </div>
              <CardTitle className="text-2xl" data-testid="confirm-registration-title">
                {t("confirm.title")}
              </CardTitle>
            </>
          )}
        </CardHeader>
        <CardContent className="text-center">
          {error && (
            <div className="space-y-4">
              <Alert variant={accountCreatedNoSession ? "warning" : "destructive"}>
                <AlertCircle className="h-4 w-4" />
                <AlertDescription data-testid="confirm-registration-error">{error}</AlertDescription>
              </Alert>
              {accountCreatedNoSession && (
                <Link
                  to="/login"
                  search={{ returnTo: undefined }}
                  className="text-primary underline-offset-4 hover:underline text-sm"
                  data-testid="confirm-registration-sign-in"
                >
                  {t("signIn")}
                </Link>
              )}
            </div>
          )}
          {confirmed && (
            <p className="text-muted-foreground">{t("confirm.redirecting")}</p>
          )}
          {pending && (
            <p className="text-muted-foreground">{t("confirm.verifying")}</p>
          )}
          {idle && (
            <div className="space-y-4">
              <p className="text-muted-foreground">{t("confirm.prompt")}</p>
              <Button
                type="button"
                className="w-full"
                onClick={handleConfirm}
                data-testid="confirm-registration-button"
              >
                {t("confirm.button")}
              </Button>
            </div>
          )}
        </CardContent>
      </Card>
    </AuthSplitLayout>
  );
}
