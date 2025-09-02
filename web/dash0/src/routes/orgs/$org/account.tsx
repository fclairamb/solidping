import { createFileRoute, Outlet } from "@tanstack/react-router";
import { TabNav } from "@/components/shared/tab-nav";

export const Route = createFileRoute("/orgs/$org/account")({
  component: AccountLayout,
});

const tabs = [
  { label: "Profile", path: "/orgs/$org/account/profile" },
  { label: "Tokens", path: "/orgs/$org/account/tokens" },
];

function AccountLayout() {
  const { org } = Route.useParams();

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">Account</h1>
        <p className="text-muted-foreground">
          Manage your personal settings and access tokens
        </p>
      </div>
      <TabNav tabs={tabs} org={org} />
      <Outlet />
    </div>
  );
}
