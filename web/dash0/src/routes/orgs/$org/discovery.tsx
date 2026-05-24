import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/discovery")({
  component: DiscoveryLayout,
});

function DiscoveryLayout() {
  return <Outlet />;
}
