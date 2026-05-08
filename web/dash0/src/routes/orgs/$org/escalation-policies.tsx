import { createFileRoute, Outlet } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/escalation-policies")({
  component: EscalationPoliciesLayout,
});

function EscalationPoliciesLayout() {
  return <Outlet />;
}
