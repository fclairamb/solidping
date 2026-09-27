import { createFileRoute, Outlet } from "@tanstack/react-router";
import { User2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { PageHeader } from "@/components/shared/page-header";
import { TabNav } from "@/components/shared/tab-nav";

export const Route = createFileRoute("/orgs/$org/account")({
  component: AccountLayout,
});

function AccountLayout() {
  const { t } = useTranslation(["account", "nav"]);
  const { org } = Route.useParams();

  const tabs = [
    { label: t("nav:profile"), path: "/orgs/$org/account/profile" },
    { label: t("nav:security"), path: "/orgs/$org/account/security" },
    { label: t("nav:sessions"), path: "/orgs/$org/account/sessions" },
    { label: t("nav:tokens"), path: "/orgs/$org/account/tokens" },
    { label: t("nav:organizations"), path: "/orgs/$org/account/organizations" },
    { label: t("nav:ai"), path: "/orgs/$org/account/mcp" },
    { label: "Notifications", path: "/orgs/$org/account/notifications" },
  ];

  return (
    <div className="space-y-6">
      <PageHeader
        icon={User2}
        title={t("account:layout.title")}
        description={t("account:layout.subtitle")}
      />
      <TabNav tabs={tabs} org={org} />
      <Outlet />
    </div>
  );
}
