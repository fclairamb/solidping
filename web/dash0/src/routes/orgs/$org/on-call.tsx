import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/on-call")({
  component: OnCallLayout,
});

function OnCallLayout() {
  return <Outlet />;
}
