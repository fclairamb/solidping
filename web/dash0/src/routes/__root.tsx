import { createRootRouteWithContext, Link, Outlet } from "@tanstack/react-router";
import type { QueryClient } from "@tanstack/react-query";
import { Toaster } from "@/components/ui/sonner";
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
    </>
  );
}

function NotFound() {
  return (
    <AuroraPanel className="min-h-screen">
      <div className="flex flex-1 items-center justify-center p-6">
        <div className="glass max-w-md space-y-4 rounded-3xl p-10 text-center">
          <p className="text-5xl font-bold tracking-tight">404</p>
          <h1 className="text-xl font-semibold">Page not found</h1>
          <p className="text-sm text-white/70">
            The page you’re looking for doesn’t exist or may have moved.
          </p>
          <Button asChild className="mt-2">
            <Link to="/">Back home</Link>
          </Button>
        </div>
      </div>
    </AuroraPanel>
  );
}
