import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/account/organizations")({
  component: OrganizationsLayout,
});

function OrganizationsLayout() {
  return <Outlet />;
}
