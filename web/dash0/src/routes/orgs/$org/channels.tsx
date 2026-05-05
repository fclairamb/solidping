import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/channels")({
  component: ChannelsLayout,
});

function ChannelsLayout() {
  return <Outlet />;
}
