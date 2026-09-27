import { createRootRouteWithContext, Outlet } from "@tanstack/react-router";
import type { QueryClient } from "@tanstack/react-query";

interface RouterContext {
  queryClient: QueryClient;
}

// The root route is the bare Outlet: every route below supplies its own chrome.
export const Route = createRootRouteWithContext<RouterContext>()({
  component: Outlet,
});
