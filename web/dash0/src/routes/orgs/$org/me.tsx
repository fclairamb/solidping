import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/me")({
  component: MeLayout,
});

function MeLayout() {
  return <Outlet />;
}
