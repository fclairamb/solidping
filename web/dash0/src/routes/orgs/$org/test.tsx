import { createFileRoute, Outlet } from "@tanstack/react-router";
import { useVersion } from "@/api/hooks";
import { TabNav } from "@/components/shared/tab-nav";

export const Route = createFileRoute("/orgs/$org/test")({
  component: TestToolsLayout,
});

const tabs = [
  { label: "Templates", path: "/orgs/$org/test/templates" },
  { label: "Bulk Checks", path: "/orgs/$org/test/bulk" },
  { label: "Generate Data", path: "/orgs/$org/test/generate" },
  { label: "Reset", path: "/orgs/$org/test/reset" },
];

function TestToolsLayout() {
  const { org } = Route.useParams();
  const { data: versionData } = useVersion();
  const isTestMode = versionData?.runMode === "test";

  if (!isTestMode) {
    return (
      <div className="p-6">
        <p className="text-muted-foreground">
          Test tools are only available when running in test mode
          (SP_RUNMODE=test).
        </p>
      </div>
    );
  }

  return (
    <div className="p-6 space-y-6">
      <div>
        <h1 className="text-2xl font-bold">Test Tools</h1>
        <p className="text-muted-foreground">
          Create and manage test checks
        </p>
      </div>
      <TabNav tabs={tabs} org={org} />
      <Outlet />
    </div>
  );
}
