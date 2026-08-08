import { createFileRoute, Navigate } from "@tanstack/react-router";
import { useAuth } from "@/contexts/AuthContext";

type DeviceRedirectSearch = {
  /**
   * The one-time code, pre-filled when the CLI's `verification_uri_complete`
   * was followed. RFC 8628 names this query parameter `user_code`, so it stays
   * snake_case here even though the rest of the app uses camelCase — this URL
   * is printed by the CLI and typed by humans.
   */
  user_code?: string;
};

/**
 * Org-less landing route for the device-authorization consent flow
 * (spec 2026-08-08-02). This is the short, typeable `verification_uri` the CLI
 * prints (`{base}/dash0/device`), so it cannot carry an org segment: it
 * resolves the org from the auth context and forwards to the real consent page
 * under `/orgs/$org/...`, exactly as `/mcp` does.
 *
 * Forwarding into the org layout is what gives a logged-out visitor the normal
 * login-with-`returnTo` bounce, so approving from a phone that is not signed in
 * yet lands back here after login instead of dead-ending.
 */
export const Route = createFileRoute("/device")({
  validateSearch: (search: Record<string, unknown>): DeviceRedirectSearch => ({
    user_code:
      typeof search.user_code === "string" ? search.user_code : undefined,
  }),
  component: DeviceRedirect,
});

function DeviceRedirect() {
  const { org, isLoading } = useAuth();
  const search = Route.useSearch();

  // Wait for auth to resolve so a logged-in user lands on their own org rather
  // than the "default" fallback.
  if (isLoading) {
    return null;
  }

  return (
    <Navigate
      to="/orgs/$org/account/device"
      params={{ org: org || "default" }}
      search={search}
    />
  );
}
