import { createFileRoute, Outlet, useNavigate } from "@tanstack/react-router";
import { useAuth } from "@/contexts/AuthContext";
import { TabNav } from "@/components/shared/tab-nav";

export const Route = createFileRoute("/orgs/$org/organization")({
  component: OrganizationLayout,
});

const tabs = [
  { label: "Invitations", path: "/orgs/$org/organization/invitations" },
  { label: "Settings", path: "/orgs/$org/organization/settings" },
];

function OrganizationLayout() {
  const { org } = Route.useParams();
  const { user } = useAuth();
  const navigate = useNavigate();

  if (!user?.isAdmin) {
    navigate({ to: "/orgs/$org", params: { org }, replace: true });
    return null;
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">Organization</h1>
        <p className="text-muted-foreground">
          Manage your organization settings and members
        </p>
      </div>
      <TabNav tabs={tabs} org={org} />
      <Outlet />
    </div>
  );
}
