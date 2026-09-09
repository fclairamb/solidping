import { createFileRoute, Navigate } from "@tanstack/react-router";
import { parseDemoFlag } from "@/lib/demo";

export const Route = createFileRoute("/login")({
  // Capture returnTo so it survives the redirect to the org login. The
  // embedded MCP OAuth authorization server bounces session-less /authorize
  // requests to exactly this route with the authorize URL as returnTo
  // (server/internal/oauth/authorize.go redirectToLogin) — dropping it here
  // dead-ends the whole MCP connect flow on the dashboard.
  //
  // `demo` rides along for the same reason: `/d/login?demo=true` is the
  // deep link that reads naturally and is the one we publish, and forwarding
  // only returnTo dropped the flag here so the visitor landed on an ordinary
  // login form (spec 2026-09-07-02).
  //
  // Both keys are OPTIONAL in the emitted type (`?:`, not `| undefined`):
  // every existing `navigate({ to: "/login", search: { returnTo } })` in the
  // app predates the demo flag and must keep compiling without naming it.
  validateSearch: (
    search: Record<string, unknown>,
  ): { returnTo?: string; demo?: true } => ({
    returnTo: typeof search.returnTo === "string" ? search.returnTo : undefined,
    demo: parseDemoFlag(search.demo),
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
  const { returnTo, demo } = Route.useSearch();
  const org = getStoredOrg() || "default";
  return (
    <Navigate
      to="/orgs/$org/login"
      params={{ org }}
      // The org in the path is irrelevant to a demo entry: the login page
      // signs into the *configured* demo org whatever page it is rendered on.
      // A returnTo captured alongside the flag is still forwarded — the demo
      // auto-login ignores it, and an ordinary sign-in on the same page (the
      // flag-less case, or an instance with no demo) still needs it.
      search={{ session_expired: false, returnTo, demo }}
    />
  );
}
