import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute(
  "/orgs/$org/maintenance-windows/$maintenanceWindowUid",
)({
  component: MaintenanceWindowDetailLayout,
});

function MaintenanceWindowDetailLayout() {
  return <Outlet />;
}
