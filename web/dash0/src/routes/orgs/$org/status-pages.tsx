import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/status-pages")({
  component: StatusPagesLayout,
});

function StatusPagesLayout() {
  return <Outlet />;
}
