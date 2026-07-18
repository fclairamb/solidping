import { createFileRoute, Outlet } from "@tanstack/react-router";

// Thin layout: the actual private-locations page lives in
// organization.private-locations.index.tsx, and the guided "Register an
// agent" wizard in organization.private-locations.register.tsx nests here as
// a sibling child route — this file's only job is to render the Outlet so
// either can display. Mirrors checks.tsx.
export const Route = createFileRoute("/orgs/$org/organization/private-locations")({
  component: PrivateLocationsLayout,
});

function PrivateLocationsLayout() {
  return <Outlet />;
}
