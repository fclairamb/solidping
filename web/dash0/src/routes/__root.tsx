import { createRootRouteWithContext, Link, Outlet } from "@tanstack/react-router";
import type { QueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Toaster } from "@/components/ui/sonner";
import { AnalyticsProvider } from "@/components/shared/analytics-provider";
import { AuroraPanel } from "@/components/ui/aurora-panel";
import { Button } from "@/components/ui/button";

interface AuthContext {
  user: { email: string; name?: string; avatarUrl?: string; roles: string[]; isAdmin: boolean } | null;
  org: string | null;
  organizations: { slug: string; name?: string; role: string }[];
  isAuthenticated: boolean;
  isLoading: boolean;
}

interface RouterContext {
  queryClient: QueryClient;
  auth: AuthContext;
}

export const Route = createRootRouteWithContext<RouterContext>()({
  component: RootLayout,
  notFoundComponent: NotFound,
});

function RootLayout() {
  return (
    <>
      <Outlet />
      <Toaster position="top-right" />
      {/* Renders nothing; loads PostHog only when the server says it is
          configured (spec 2026-08-02-08). */}
      <AnalyticsProvider />
    </>
  );
}

function NotFound() {
  const { t } = useTranslation("common");

  return (
    <AuroraPanel className="min-h-screen">
      <div className="flex flex-1 items-center justify-center p-6">
        <div className="glass max-w-md space-y-4 rounded-3xl p-10 text-center">
          <p className="text-5xl font-bold tracking-tight">404</p>
          <h1 className="text-xl font-semibold">{t("notFoundPage.title")}</h1>
          <p className="text-sm text-white/70">
            {t("notFoundPage.description")}
          </p>
          <Button asChild className="mt-2">
            <Link to="/">{t("notFoundPage.backHome")}</Link>
          </Button>
        </div>
      </div>
    </AuroraPanel>
  );
}
