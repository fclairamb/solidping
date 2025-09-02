import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/incidents")({
  component: IncidentsLayout,
});

function IncidentsLayout() {
  return <Outlet />;
}
