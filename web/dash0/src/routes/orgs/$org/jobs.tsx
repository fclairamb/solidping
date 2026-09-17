import { createFileRoute, Outlet, useNavigate } from "@tanstack/react-router";

import { useAuth } from "@/contexts/AuthContext";

export const Route = createFileRoute("/orgs/$org/jobs")({
  component: JobsLayout,
});

// JobsLayout guards the super-admin-only Jobs section (spec 2026-09-16-07:
// queue internals are noise for a first-time org admin, so the sidebar entry
// and this page are scoped to `isSuperAdmin`). Mirrors the organization
// layout: once auth has loaded, anyone else is sent back to the org home with
// `replace` (403, never a redirect loop — per wiki/conventions/frontend-errors).
//
// The BACKEND gates stay `RequireOrgAdmin` on purpose — see the spec. This is
// a dashboard-clutter change, not an API-privilege change, and tightening the
// server would break `sp jobs` / `sp check-jobs` for org admins.
function JobsLayout() {
  const { org } = Route.useParams();
  const { user, isLoading } = useAuth();
  const navigate = useNavigate();

  if (isLoading) {
    return null;
  }

  if (!user?.isSuperAdmin) {
    navigate({ to: "/orgs/$org", params: { org }, replace: true });
    return null;
  }

  return <Outlet />;
}
