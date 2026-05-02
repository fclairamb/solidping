import { useEffect, useRef, useState } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Loader2 } from "lucide-react";

import { apiFetch } from "@/api/client";
import { useAuth } from "@/contexts/AuthContext";

interface CompleteSearch {
  code?: string;
}

export const Route = createFileRoute("/auth/slack/complete")({
  validateSearch: (search: Record<string, unknown>): CompleteSearch => ({
    code: typeof search.code === "string" ? search.code : undefined,
  }),
  component: SlackInstallComplete,
});

interface ExchangeResponse {
  accessToken: string;
  refreshToken: string;
  orgSlug: string;
  userUid: string;
}

const installErrorPage = "https://www.solidping.io/saas/install-error";

function SlackInstallComplete() {
  const { code } = Route.useSearch();
  const navigate = useNavigate();
  const { loginWithOAuth } = useAuth();
  const ranRef = useRef(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (ranRef.current) return;
    ranRef.current = true;

    if (!code) {
      window.location.href = `${installErrorPage}?reason=state_invalid`;
      return;
    }

    void (async () => {
      try {
        const data = await apiFetch<ExchangeResponse>(
          "/api/v1/auth/slack/exchange",
          {
            method: "POST",
            body: JSON.stringify({ code }),
            skipAuth: true,
          }
        );

        await loginWithOAuth(data.accessToken, data.orgSlug);

        navigate({
          to: "/orgs/$org",
          params: { org: data.orgSlug },
          replace: true,
        });
      } catch (err) {
        setError(err instanceof Error ? err.message : "Unexpected error");
        window.location.href = `${installErrorPage}?reason=oauth_failed`;
      }
    })();
  }, [code, loginWithOAuth, navigate]);

  return (
    <div className="flex min-h-screen items-center justify-center">
      <div className="flex flex-col items-center gap-3 text-center">
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
        <p className="text-sm text-muted-foreground">
          {error ?? "Finishing your Slack install…"}
        </p>
      </div>
    </div>
  );
}
