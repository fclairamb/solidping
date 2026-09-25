import { useEffect, useRef, useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { AlertCircle, Loader2 } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { AuthSplitLayout } from "@/components/layout/auth-split-layout";
import { useAuth } from "@/contexts/AuthContext";
import { DASH_BASE } from "@/lib/base-path";
import {
  exchangeHandoffCode,
  HANDOFF_COMPLETE_PATH,
  resolveHandoffLanding,
} from "@/lib/auth-handoff";

interface CompleteSearch {
  code?: string;
  membershipPending?: string;
}

// Landing route of every federated login (spec 2026-09-25-12). The provider
// callback redirects here with a single-use handoff code instead of the
// session tokens; see lib/auth-handoff.ts.
export const Route = createFileRoute("/auth/complete")({
  validateSearch: (search: Record<string, unknown>): CompleteSearch => ({
    code: typeof search.code === "string" && search.code ? search.code : undefined,
    membershipPending:
      typeof search.membershipPending === "string" && search.membershipPending
        ? search.membershipPending
        : undefined,
  }),
  component: AuthCompletePage,
});

function AuthCompletePage() {
  const { t } = useTranslation("auth");
  const { code, membershipPending } = Route.useSearch();
  const navigate = useNavigate();
  const { applyLoginResponse } = useAuth();
  const [exchangeFailed, setExchangeFailed] = useState(false);
  const firedCodeRef = useRef<string | null>(null);
  // No code at all (a bookmarked or hand-typed URL) is a failure up front.
  const failed = !code || exchangeFailed;

  useEffect(() => {
    if (!code || firedCodeRef.current === code) return;
    firedCodeRef.current = code;

    // The code is single use and about to be spent, but until then it is a
    // credential: take it out of the address bar and this history entry right
    // away. Only the non-secret flag stays.
    const cleanSearch = membershipPending
      ? `?membershipPending=${encodeURIComponent(membershipPending)}`
      : "";
    window.history.replaceState(
      window.history.state,
      "",
      `${DASH_BASE}${HANDOFF_COMPLETE_PATH}${cleanSearch}`,
    );

    void (async () => {
      try {
        const data = await exchangeHandoffCode(code);
        // Same funnel as a password login: stores the session, and sends a
        // flagged account straight to the forced password rotation.
        await applyLoginResponse(data);
        if (data.user.mustChangePassword) return;

        const landing = resolveHandoffLanding(data, membershipPending, DASH_BASE);
        if ("href" in landing) {
          // An in-app deep path (or the MCP consent bounce through the login
          // page) outside this router's param shapes: a full navigation, as
          // the login page does.
          window.location.replace(landing.href);
        } else if (landing.to === "/no-org") {
          navigate({ to: "/no-org", search: landing.search, replace: true });
        } else {
          navigate({ to: landing.to, params: landing.params, replace: true });
        }
      } catch {
        setExchangeFailed(true);
      }
    })();
  }, [code, membershipPending, applyLoginResponse, navigate]);

  return (
    <AuthSplitLayout>
      <Card className="w-full max-w-md border-t-4 border-t-brand">
        <CardHeader className="text-center">
          <div className="flex justify-center mb-4">
            {failed ? (
              <AlertCircle className="h-12 w-12 text-destructive" />
            ) : (
              <Loader2 className="h-12 w-12 animate-spin text-primary" />
            )}
          </div>
          <CardTitle className="text-2xl">
            {failed ? t("authComplete.failedTitle") : t("authComplete.finishing")}
          </CardTitle>
        </CardHeader>
        <CardContent className="text-center">
          {failed && (
            <div className="space-y-4">
              <Alert variant="destructive">
                <AlertCircle className="h-4 w-4" />
                <AlertDescription>{t("authComplete.failedDescription")}</AlertDescription>
              </Alert>
              <Link
                to="/login"
                search={{ returnTo: undefined }}
                className="text-primary underline-offset-4 hover:underline text-sm"
                data-testid="auth-complete-back-to-login"
              >
                {t("backToLogin")}
              </Link>
            </div>
          )}
        </CardContent>
      </Card>
    </AuthSplitLayout>
  );
}
