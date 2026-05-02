import { createFileRoute, Outlet } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { useVersion } from "@/api/hooks";
import { TabNav } from "@/components/shared/tab-nav";

export const Route = createFileRoute("/orgs/$org/test")({
  component: TestToolsLayout,
});

function TestToolsLayout() {
  const { t } = useTranslation("nav");
  const { org } = Route.useParams();
  const { data: versionData } = useVersion();
  const isTestMode = versionData?.runMode === "test";

  const tabs = [
    { label: t("test.tabs.templates"), path: "/orgs/$org/test/templates" },
    { label: t("test.tabs.bulk"), path: "/orgs/$org/test/bulk" },
    { label: t("test.tabs.generate"), path: "/orgs/$org/test/generate" },
    { label: t("test.tabs.reset"), path: "/orgs/$org/test/reset" },
  ];

  if (!isTestMode) {
    return (
      <div className="p-6">
        <p className="text-muted-foreground">{t("test.notAvailable")}</p>
      </div>
    );
  }

  return (
    <div className="p-6 space-y-6">
      <div>
        <h1 className="text-2xl font-bold">{t("test.layoutTitle")}</h1>
        <p className="text-muted-foreground">{t("test.layoutSubtitle")}</p>
      </div>
      <TabNav tabs={tabs} org={org} />
      <Outlet />
    </div>
  );
}
