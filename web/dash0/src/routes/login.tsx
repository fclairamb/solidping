import { createFileRoute, Navigate } from "@tanstack/react-router";

export const Route = createFileRoute("/login")({
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
  const org = getStoredOrg() || "default";
  return (
    <Navigate
      to="/orgs/$org/login"
      params={{ org }}
      search={{ session_expired: false, returnTo: undefined }}
    />
  );
}
