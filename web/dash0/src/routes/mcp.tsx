import { createFileRoute, Navigate } from "@tanstack/react-router";
import { useAuth } from "@/contexts/AuthContext";

type McpRedirectSearch = {
  /** "get" when the backend redirected a browser off GET /api/v1/mcp. */
  from?: string;
};

// Org-less landing route for the MCP setup page. The backend redirects a
// browser hitting GET /api/v1/mcp to /dash0/mcp?from=get without knowing the
// org (the request is unauthenticated), so this route — like the root
// redirect in index.tsx — resolves the org from the auth context and
// forwards to the Account MCP page, preserving the query string. Logged-out
// users then go through the login-with-returnTo flow via the /orgs/$org
// guard — which drops the returnTo for anyone whose org is not the guessed
// "default" slug (resolveDestination's org-match rule). Not fixed here (out of
// scope); routes/device.tsx shows the mechanism if it ever needs to be:
// bounce to /login with an org-less returnTo that resolveDestination honors
// explicitly (isDeviceVerificationReturnTo).
export const Route = createFileRoute("/mcp")({
  validateSearch: (search: Record<string, unknown>): McpRedirectSearch => ({
    from: typeof search.from === "string" ? search.from : undefined,
  }),
  component: McpRedirect,
});

function McpRedirect() {
  const { org, isLoading } = useAuth();
  const search = Route.useSearch();

  // Wait for auth to resolve so a logged-in user lands on their own org
  // instead of the "default" fallback.
  if (isLoading) {
    return null;
  }

  return (
    <Navigate to="/orgs/$org/account/mcp" params={{ org: org || "default" }} search={search} />
  );
}
