import { createFileRoute, Navigate } from "@tanstack/react-router";
import { useQueryClient } from "@tanstack/react-query";
import { readCachedDemoOrgSlug } from "@/api/public-config";
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
  const queryClient = useQueryClient();
  // Since spec 2026-09-12-01 the demo is signed into ONLY from the demo org's
  // own login page, so landing a demo visitor anywhere else costs an extra hop.
  // Aim straight at the demo org when the public-config document happens to be
  // cached already; never wait for it — the login page hops on its own, and a
  // redirect that blocked on the network would be a blank screen on the
  // product's front door.
  const cachedDemoOrg = demo ? readCachedDemoOrgSlug(queryClient) : undefined;
  const org = cachedDemoOrg || getStoredOrg() || "default";
  return (
    <Navigate
      to="/orgs/$org/login"
      params={{ org }}
      // A returnTo captured alongside the flag is still forwarded — the demo
      // auto-login ignores it, and an ordinary sign-in on the same page (the
      // flag-less case, or an instance with no demo) still needs it.
      search={{ session_expired: false, returnTo, demo }}
    />
  );
}
