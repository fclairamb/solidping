import { createFileRoute, Navigate } from "@tanstack/react-router";

export const Route = createFileRoute("/login")({
  component: LoginRedirect,
});

// Redirect old /login to org-based login with default org
function LoginRedirect() {
  return (
    <Navigate
      to="/orgs/$org/login"
      params={{ org: "test" }}
      search={{ session_expired: false, returnTo: undefined }}
    />
  );
}
