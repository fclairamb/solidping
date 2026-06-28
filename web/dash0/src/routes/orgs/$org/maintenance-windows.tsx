import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/maintenance-windows")({
  component: MaintenanceWindowsLayout,
});

function MaintenanceWindowsLayout() {
  return <Outlet />;
}
