import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/slos/$uid")({
  component: SloDetailLayout,
});

function SloDetailLayout() {
  return <Outlet />;
}
