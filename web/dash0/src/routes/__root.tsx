import { createRootRouteWithContext, Outlet } from "@tanstack/react-router";
import type { QueryClient } from "@tanstack/react-query";
import { Toaster } from "@/components/ui/sonner";

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
});

function RootLayout() {
  return (
    <>
      <Outlet />
      <Toaster position="top-right" />
    </>
  );
}
