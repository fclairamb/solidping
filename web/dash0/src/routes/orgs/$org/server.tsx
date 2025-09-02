import { createFileRoute, Outlet, useNavigate } from "@tanstack/react-router";
import { useAuth } from "@/contexts/AuthContext";
import { TabNav } from "@/components/shared/tab-nav";

export const Route = createFileRoute("/orgs/$org/server")({
  component: ServerLayout,
});

const tabs = [
  { label: "Web", path: "/orgs/$org/server/web" },
  { label: "Mail", path: "/orgs/$org/server/mail" },
  { label: "Authentication", path: "/orgs/$org/server/auth" },
  { label: "Performance", path: "/orgs/$org/server/performance" },
];

function ServerLayout() {
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

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">Server Settings</h1>
        <p className="text-muted-foreground">
          Manage server-wide configuration parameters
        </p>
      </div>
      <TabNav tabs={tabs} org={org} />
      <Outlet />
    </div>
  );
}
