import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/status-updates/$updateUid")({
  component: StatusUpdateLayout,
});

function StatusUpdateLayout() {
  return <Outlet />;
}
