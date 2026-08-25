import type { ReactNode } from "react";

import { Link, Navigate } from "@tanstack/react-router";
import { ShieldX } from "lucide-react";
import { useTranslation } from "react-i18next";

import { useAuth } from "@/contexts/AuthContext";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

/**
 * Auth gate for the unlinked `/support` route (spec 2026-08-22-02).
 *
 * Three states, and the difference between the last two is the whole point:
 *
 *   - still loading   → render nothing. Mistaking "loading" for "logged out"
 *                       bounces a legitimate operator to the login page on
 *                       every hard refresh.
 *   - not logged in   → /login with returnTo, per the repo's 401 convention.
 *   - not a superadmin→ "Permission Denied", rendered in place and NEVER a
 *                       redirect. A redirect here loops forever for an
 *                       authenticated-but-unauthorized user, which is exactly
 *                       what wiki/conventions/frontend-errors.md forbids.
 *
 * Hiding the route from the menu is discoverability, not security: the API is
 * RequireSuperAdmin on every endpoint and that is the real boundary.
 */
export function SupportGate({ children }: { children: ReactNode }) {
  const { user, org, isAuthenticated, isLoading } = useAuth();

  if (isLoading) {
    return null;
  }

  if (!isAuthenticated) {
    const basepath = import.meta.env.VITE_BASE_URL || "";

    return <Navigate to="/login" search={{ returnTo: `${basepath}/support` }} />;
  }

  if (!user?.isSuperAdmin) {
    return <SupportPermissionDenied org={org} />;
  }

  return <>{children}</>;
}

/**
 * Permission Denied for a route that has no `org` path param.
 *
 * The shared `PermissionDenied` requires an org for its "Return to Dashboard"
 * link; `/support` is instance-level and has none, so the link falls back to the
 * viewer's current org and is dropped entirely when they have none at all.
 */
function SupportPermissionDenied({ org }: { org: string | null }) {
  const { t } = useTranslation();

  return (
    <div className="py-12 text-center" data-testid="support-permission-denied">
      <Card className="mx-auto max-w-md">
        <CardHeader>
          <div className="mb-2 flex justify-center">
            <ShieldX className="h-10 w-10 text-destructive" />
          </div>
          <CardTitle>{t("permissionDenied")}</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="mb-4 text-muted-foreground">
            {t("permissionDeniedDescription")}
          </p>
          {org ? (
            <Link to="/orgs/$org" params={{ org }}>
              <Button variant="outline">{t("returnToDashboard")}</Button>
            </Link>
          ) : null}
        </CardContent>
      </Card>
    </div>
  );
}
