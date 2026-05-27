import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/discovery/$jobUid")({
  component: () => <Outlet />,
});
