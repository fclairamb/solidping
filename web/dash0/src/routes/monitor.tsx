import { createFileRoute, Navigate } from "@tanstack/react-router";
import { useAuth } from "@/contexts/AuthContext";
import {
  clearPendingCheck,
  filterMonitorQuery,
  monitorReturnTo,
  savePendingCheck,
  type MonitorQuery,
} from "@/lib/pending-check";

/**
 * The org-less "monitor this" deep link (spec 2026-10-14-03), published by the
 * website's free tools. Forwards the allowlisted prefill keys to the new-check
 * form of whatever org the visitor ends up in; signed-out visitors go through
 * login/registration first and the prefill survives it (localStorage).
 */
export const Route = createFileRoute("/monitor")({
  validateSearch: (search: Record<string, unknown>): MonitorQuery =>
    filterMonitorQuery(search),
  component: MonitorRedirect,
});

function MonitorRedirect() {
  // Filtered here too: the search the component reads still carries the raw
  // URL keys, and the allowlist must hold wherever the query is forwarded from.
  const query = filterMonitorQuery(Route.useSearch());
  const { org, user, isAuthenticated, isLoading } = useAuth();

  if (isLoading) {
    return null;
  }

  if (!isAuthenticated) {
    // Kept for the registration path: the email confirmation can happen in
    // another tab, where this URL is gone.
    savePendingCheck(query);
    const basepath = import.meta.env.VITE_BASE_URL || "";
    return (
      <Navigate
        to="/login"
        search={{ returnTo: monitorReturnTo(basepath, query) }}
        replace
      />
    );
  }

  if (!org) {
    // Signed in with no org yet: the no-org flow consumes the pending prefill
    // once the org is created.
    savePendingCheck(query);
    return <Navigate to="/no-org" search={{ membershipPending: undefined }} replace />;
  }

  // This visit handles the link: a prefill saved by an earlier signed-out hop
  // must not fire a second time on `/`.
  clearPendingCheck();
  if (user?.isDemo) {
    return <Navigate to="/orgs/$org" params={{ org }} replace />;
  }
  return (
    <Navigate
      to="/orgs/$org/checks/new"
      params={{ org }}
      // checks.new normalises each value itself (toInt etc.).
      search={query as never}
      replace
    />
  );
}
