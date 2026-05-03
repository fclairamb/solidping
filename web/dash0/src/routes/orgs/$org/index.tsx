import { createFileRoute } from "@tanstack/react-router";
import { OrgDashboardPage } from "@/components/dashboard/dashboard-page";

export const Route = createFileRoute("/orgs/$org/")({
  beforeLoad: ({ location }) => {
    // OAuth callback drops `?access_token=` here for OrgLayout to consume.
    // We let the layout handle it; otherwise we render the dashboard.
    if (new URLSearchParams(location.searchStr || "").has("access_token")) {
      return;
    }
  },
  component: DashboardRoute,
});

function DashboardRoute() {
  const { org } = Route.useParams();
  return <OrgDashboardPage org={org} />;
}
