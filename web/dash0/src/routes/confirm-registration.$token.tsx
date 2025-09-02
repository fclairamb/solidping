import { useEffect, useState } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { AlertCircle, CheckCircle2, Loader2 } from "lucide-react";
import { setToken } from "@/api/client";
import { useConfirmRegistration } from "@/api/hooks";

export const Route = createFileRoute("/confirm-registration/$token")({
  component: ConfirmRegistrationPage,
});

function ConfirmRegistrationPage() {
  const { token } = Route.useParams();
  const navigate = useNavigate();
  const confirmRegistration = useConfirmRegistration();
  const [error, setError] = useState<string | null>(null);
  const [confirmed, setConfirmed] = useState(false);

  useEffect(() => {
    if (!token || confirmed || confirmRegistration.isPending) return;

    confirmRegistration
      .mutateAsync({ token })
      .then((data) => {
        setConfirmed(true);
        setToken(data.accessToken);
        const orgSlug = data.organization?.slug;
        setTimeout(() => {
          if (orgSlug) {
            navigate({ to: "/orgs/$org", params: { org: orgSlug } });
          } else {
            navigate({ to: "/no-org" });
          }
        }, 1500);
      })
      .catch((err) => {
        setError(err.message || "Failed to confirm registration");
      });
  }, [token]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="min-h-screen flex items-center justify-center bg-background p-4">
      <Card className="w-full max-w-md">
        <CardHeader className="text-center">
          {error ? (
            <>
              <div className="flex justify-center mb-4">
                <AlertCircle className="h-12 w-12 text-destructive" />
              </div>
              <CardTitle className="text-2xl">Confirmation failed</CardTitle>
            </>
          ) : confirmed ? (
            <>
              <div className="flex justify-center mb-4">
                <CheckCircle2 className="h-12 w-12 text-green-500" />
              </div>
              <CardTitle className="text-2xl">Account confirmed</CardTitle>
            </>
          ) : (
            <>
              <div className="flex justify-center mb-4">
                <Loader2 className="h-12 w-12 animate-spin text-primary" />
              </div>
              <CardTitle className="text-2xl">
                Confirming your account...
              </CardTitle>
            </>
          )}
        </CardHeader>
        <CardContent className="text-center">
          {error && (
            <Alert variant="destructive">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          {confirmed && (
            <p className="text-muted-foreground">
              Redirecting you to your dashboard...
            </p>
          )}
          {!error && !confirmed && (
            <p className="text-muted-foreground">
              Please wait while we verify your email...
            </p>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
