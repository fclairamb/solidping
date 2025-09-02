import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/status-pages/$statusPageUid")({
  component: StatusPageDetailLayout,
});

function StatusPageDetailLayout() {
  return <Outlet />;
}
