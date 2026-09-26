import { createFileRoute } from "@tanstack/react-router";
import { OrgDashboardPage } from "@/components/dashboard/dashboard-page";

export const Route = createFileRoute("/orgs/$org/")({
  component: DashboardRoute,
});

function DashboardRoute() {
  const { org } = Route.useParams();
  return <OrgDashboardPage org={org} />;
}
