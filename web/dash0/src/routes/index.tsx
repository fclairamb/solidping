import { createFileRoute, Navigate } from "@tanstack/react-router";
import { useAuth } from "@/contexts/AuthContext";

export const Route = createFileRoute("/")({
  component: RootRedirect,
});

function RootRedirect() {
  const { org } = useAuth();
  // No resolved org — the user belongs to none (or just deleted their last
  // one). Sending them to /orgs/default only produced a 404 on installs with
  // no `default` org; the empty state is the honest destination.
  if (!org) {
    return <Navigate to="/no-org" search={{ membershipPending: undefined }} />;
  }
  return <Navigate to="/orgs/$org" params={{ org }} />;
}
