import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/on-call/$uid")({
  component: OnCallUidLayout,
});

function OnCallUidLayout() {
  return <Outlet />;
}
