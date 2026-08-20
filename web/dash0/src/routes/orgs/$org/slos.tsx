import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/slos")({
  component: SlosLayout,
});

function SlosLayout() {
  return <Outlet />;
}
