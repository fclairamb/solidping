import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/on-call/$slug")({
  component: OnCallSlugLayout,
});

function OnCallSlugLayout() {
  return <Outlet />;
}
