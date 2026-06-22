import { createFileRoute, Outlet, useNavigate } from "@tanstack/react-router";

import { useAuth } from "@/contexts/AuthContext";

export const Route = createFileRoute("/orgs/$org/jobs")({
  component: JobsLayout,
});

// JobsLayout guards the admin-only Jobs section. Mirrors the organization
// layout: once auth has loaded, a non-admin is sent back to the org home with
// `replace` (403, never a redirect loop — per wiki/conventions/frontend-errors).
function JobsLayout() {
  const { org } = Route.useParams();
  const { user, isLoading } = useAuth();
  const navigate = useNavigate();

  if (isLoading) {
    return null;
  }

  if (!user?.isAdmin) {
    navigate({ to: "/orgs/$org", params: { org }, replace: true });
    return null;
  }

  return <Outlet />;
}
