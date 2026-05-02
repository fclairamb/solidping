import { createFileRoute, Outlet, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/contexts/AuthContext";
import { TabNav } from "@/components/shared/tab-nav";

export const Route = createFileRoute("/orgs/$org/organization")({
  component: OrganizationLayout,
});

function OrganizationLayout() {
  const { t } = useTranslation(["org", "nav"]);
  const { org } = Route.useParams();
  const { user } = useAuth();
  const navigate = useNavigate();

  const tabs = [
    { label: t("nav:invitations"), path: "/orgs/$org/organization/invitations" },
    { label: t("nav:settings"), path: "/orgs/$org/organization/settings" },
  ];

  if (!user?.isAdmin) {
    navigate({ to: "/orgs/$org", params: { org }, replace: true });
    return null;
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">{t("org:layout.title")}</h1>
        <p className="text-muted-foreground">{t("org:layout.subtitle")}</p>
      </div>
      <TabNav tabs={tabs} org={org} />
      <Outlet />
    </div>
  );
}
