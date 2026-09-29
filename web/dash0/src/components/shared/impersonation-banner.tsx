import { useTranslation } from "react-i18next";
import { Eye, LogOut } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/contexts/AuthContext";
import { exitImpersonation, getImpersonation } from "@/lib/impersonation";

/**
 * ImpersonationBanner is shown on every org page while a super admin views the
 * dashboard as another user (spec 2026-09-29-03).
 *
 * NOT DISMISSIBLE, deliberately: an admin who forgets they are acting as
 * someone else is exactly the accident this exists to prevent. It renders when
 * either signal says so, /auth/me (the server's view, which covers a tab that
 * lost its sessionStorage) or this tab's stored impersonation.
 *
 * "Exit" drops the impersonation token and reloads on the admin's own session,
 * which was never touched.
 */
export function ImpersonationBanner() {
  const { t } = useTranslation(["server"]);
  const { user } = useAuth();
  const stored = getImpersonation();

  if (!user?.impersonation && !stored) {
    return null;
  }

  const email = user?.email ?? stored?.targetEmail ?? "";
  const expiresAt = user?.impersonation?.expiresAt
    ? new Date(user.impersonation.expiresAt)
    : stored
      ? new Date(stored.expiresAt)
      : null;
  const time = expiresAt
    ? expiresAt.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
    : "";

  return (
    <Alert variant="warning" className="mb-3" data-testid="impersonation-banner">
      <Eye />
      <AlertTitle className="break-words">
        {t("server:impersonation.banner.title", { email })}
      </AlertTitle>
      <AlertDescription className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
        <p>{t("server:impersonation.banner.description", { time })}</p>
        <Button
          size="sm"
          variant="outline"
          className="min-h-9 shrink-0 self-start sm:self-auto"
          onClick={() => exitImpersonation()}
          data-testid="impersonation-exit"
        >
          <LogOut className="mr-1 h-4 w-4" />
          {t("server:impersonation.banner.exit")}
        </Button>
      </AlertDescription>
    </Alert>
  );
}
