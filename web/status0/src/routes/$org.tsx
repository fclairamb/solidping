import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/$org")({
  component: OrgLayout,
});

function OrgLayout() {
  return <Outlet />;
}
