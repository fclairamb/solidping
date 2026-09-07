import { createFileRoute, Navigate } from "@tanstack/react-router";
import { useAuth } from "@/contexts/AuthContext";
import { parseDemoFlag } from "@/lib/demo";

export const Route = createFileRoute("/")({
  // `/dash0/?demo=true` is a deep link into the shared live demo, not a request
  // for whatever org this browser last visited (spec 2026-09-07-02).
  // Optional in the emitted type, deliberately: `<Link to="/">` appears in
  // several places (the root error boundary among them) and must keep
  // compiling without naming a search param it has no opinion about.
  validateSearch: (
    search: Record<string, unknown>,
  ): { demo?: true } => ({
    demo: parseDemoFlag(search.demo),
  }),
  component: RootRedirect,
});

const ORG_KEY = "solidping_org";

function getStoredOrg(): string | null {
  try {
    return localStorage.getItem(ORG_KEY);
  } catch {
    return null;
  }
}

function RootRedirect() {
  const { demo } = Route.useSearch();
  const { org } = useAuth();
  // The demo flag is answered BEFORE the session is consulted: a visitor who
  // already holds a session (their own org, or the demo itself) followed a link
  // that says "demo", and sending them to `useAuth().org` instead is exactly
  // the bug this route had. Any org slug works in the path — the login page
  // signs into the configured demo org regardless — so reuse the same
  // stored-org-else-`default` choice the root /login route makes.
  if (demo) {
    return (
      <Navigate
        to="/orgs/$org/login"
        params={{ org: getStoredOrg() || "default" }}
        search={{ session_expired: false, returnTo: undefined, demo: true }}
        replace
      />
    );
  }
  // No resolved org — the user belongs to none (or just deleted their last
  // one). Sending them to /orgs/default only produced a 404 on installs with
  // no `default` org; the empty state is the honest destination.
  if (!org) {
    return <Navigate to="/no-org" search={{ membershipPending: undefined }} />;
  }
  return <Navigate to="/orgs/$org" params={{ org }} />;
}
