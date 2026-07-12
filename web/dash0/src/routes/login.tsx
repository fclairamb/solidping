import { createFileRoute, Navigate } from "@tanstack/react-router";

export const Route = createFileRoute("/login")({
  // Capture returnTo so it survives the redirect to the org login. The
  // embedded MCP OAuth authorization server bounces session-less /authorize
  // requests to exactly this route with the authorize URL as returnTo
  // (server/internal/oauth/authorize.go redirectToLogin) — dropping it here
  // dead-ends the whole MCP connect flow on the dashboard.
  validateSearch: (search: Record<string, unknown>) => ({
    returnTo: typeof search.returnTo === "string" ? search.returnTo : undefined,
  }),
  component: LoginRedirect,
});

const ORG_KEY = "solidping_org";

function getStoredOrg(): string | null {
  try {
    return localStorage.getItem(ORG_KEY);
  } catch {
    return null;
  }
}

// Redirect old /login to org-based login. Prefer the last-visited org from
// localStorage; fall back to "default" (the prod default org slug).
function LoginRedirect() {
  const { returnTo } = Route.useSearch();
  const org = getStoredOrg() || "default";
  return (
    <Navigate
      to="/orgs/$org/login"
      params={{ org }}
      search={{ session_expired: false, returnTo }}
    />
  );
}
