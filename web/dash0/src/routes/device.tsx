import { createFileRoute, Navigate } from "@tanstack/react-router";
import { useAuth } from "@/contexts/AuthContext";
import { deviceVerificationReturnTo } from "@/lib/login-destination";

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
 * under `/orgs/$org/...`.
 *
 * A logged-out visitor is sent to /login with THIS route (code and all) as
 * `returnTo`, and `resolveDestination` honors that shape without applying its
 * org-match rule — see `isDeviceVerificationReturnTo`. Bouncing through
 * /orgs/<guess>/... instead would lose the code for every user whose org is
 * not the guessed slug, which is exactly the approve-from-your-phone path the
 * device grant exists for.
 *
 * (`/mcp` still forwards through the org guard and inherits that limitation;
 * it is unrelated to this spec, but the mechanism above is how to fix it.)
 */
export const Route = createFileRoute("/device")({
  validateSearch: (search: Record<string, unknown>): DeviceRedirectSearch => ({
    user_code:
      typeof search.user_code === "string" ? search.user_code : undefined,
  }),
  component: DeviceRedirect,
});

function DeviceRedirect() {
  const { org, isAuthenticated, isLoading } = useAuth();
  const search = Route.useSearch();

  // Wait for auth to resolve so a logged-in user lands on their own org rather
  // than a guessed fallback, and so a returning session is not mistaken for a
  // logged-out one.
  if (isLoading) {
    return null;
  }

  if (!isAuthenticated) {
    const basepath = import.meta.env.VITE_BASE_URL || "";
    return (
      <Navigate
        to="/login"
        search={{
          returnTo: deviceVerificationReturnTo(basepath, search.user_code),
        }}
      />
    );
  }

  return (
    <Navigate
      to="/orgs/$org/account/device"
      params={{ org: org || "default" }}
      search={search}
    />
  );
}
