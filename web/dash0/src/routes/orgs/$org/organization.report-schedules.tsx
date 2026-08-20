import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/organization/report-schedules")({
  component: ReportSchedulesLayout,
});

function ReportSchedulesLayout() {
  return <Outlet />;
}
