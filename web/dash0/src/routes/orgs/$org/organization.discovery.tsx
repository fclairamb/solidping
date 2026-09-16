import { createFileRoute, Outlet } from "@tanstack/react-router";

// Thin layout: the actual discovery pages live in the sibling
// organization.discovery.*.tsx files. This file's only job is to render the
// Outlet so they can display — mirrors organization.private-locations.tsx /
// organization.report-schedules.tsx. The admin guard is NOT re-implemented
// here: organization.tsx's layout already redirects a non-admin away before
// this ever mounts.
export const Route = createFileRoute("/orgs/$org/organization/discovery")({
  component: DiscoveryLayout,
});

function DiscoveryLayout() {
  return <Outlet />;
}
