import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/status-updates")({
  component: StatusUpdatesLayout,
});

function StatusUpdatesLayout() {
  return <Outlet />;
}
