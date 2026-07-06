import { createFileRoute, Outlet, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/contexts/AuthContext";
import { TabNav } from "@/components/shared/tab-nav";
import { useOrgMembershipRequests } from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/organization")({
  component: OrganizationLayout,
});

function OrganizationLayout() {
  const { t } = useTranslation(["org", "nav"]);
  const { org } = Route.useParams();
  const { user, isLoading } = useAuth();
  const navigate = useNavigate();
  const { data: pendingData } = useOrgMembershipRequests(org, {
    status: "pending",
    enabled: !!user?.isAdmin,
  });
  const pendingCount = pendingData?.data.length ?? 0;

  const tabs = [
    { label: t("nav:members"), path: "/orgs/$org/organization/members" },
    { label: t("nav:invitations"), path: "/orgs/$org/organization/invitations" },
    {
      label: t("nav:requests", "Requests"),
      path: "/orgs/$org/organization/requests",
      badge: pendingCount,
    },
    { label: t("nav:usage", "Usage"), path: "/orgs/$org/organization/usage" },
    { label: t("nav:ai", "AI assistants"), path: "/orgs/$org/organization/ai" },
    { label: t("nav:settings"), path: "/orgs/$org/organization/settings" },
  ];

  if (isLoading) {
    return null;
  }
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
