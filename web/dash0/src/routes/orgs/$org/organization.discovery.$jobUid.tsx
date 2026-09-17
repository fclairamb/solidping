import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/organization/discovery/$jobUid")({
  component: () => <Outlet />,
});
