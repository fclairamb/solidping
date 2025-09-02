import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/checks/$checkUid")({
  component: CheckDetailLayout,
});

function CheckDetailLayout() {
  return <Outlet />;
}
