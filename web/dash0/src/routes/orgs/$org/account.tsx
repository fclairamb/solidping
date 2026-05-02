import { createFileRoute, Outlet } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { TabNav } from "@/components/shared/tab-nav";

export const Route = createFileRoute("/orgs/$org/account")({
  component: AccountLayout,
});

function AccountLayout() {
  const { t } = useTranslation(["account", "nav"]);
  const { org } = Route.useParams();

  const tabs = [
    { label: t("nav:profile"), path: "/orgs/$org/account/profile" },
    { label: t("nav:tokens"), path: "/orgs/$org/account/tokens" },
  ];

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">{t("account:layout.title")}</h1>
        <p className="text-muted-foreground">{t("account:layout.subtitle")}</p>
      </div>
      <TabNav tabs={tabs} org={org} />
      <Outlet />
    </div>
  );
}
