import { createFileRoute, Outlet, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { useAuth } from "@/contexts/AuthContext";
import { TabNav } from "@/components/shared/tab-nav";

export const Route = createFileRoute("/orgs/$org/server")({
  component: ServerLayout,
});

function ServerLayout() {
  const { t } = useTranslation("server");
  const { org } = Route.useParams();
  const { user, isLoading } = useAuth();
  const navigate = useNavigate();

  const tabs = [
    { label: t("tabs.web"), path: "/orgs/$org/server/web" },
    { label: t("tabs.mail"), path: "/orgs/$org/server/mail" },
    { label: t("tabs.emailInbox"), path: "/orgs/$org/server/email-inbox" },
    { label: t("tabs.authentication"), path: "/orgs/$org/server/auth" },
    { label: t("tabs.hashing"), path: "/orgs/$org/server/hashing" },
    { label: t("tabs.slack"), path: "/orgs/$org/server/slack" },
    { label: t("tabs.discord", "Discord"), path: "/orgs/$org/server/discord" },
    { label: t("tabs.performance"), path: "/orgs/$org/server/performance" },
    { label: t("tabs.agents"), path: "/orgs/$org/server/agents" },
    { label: t("tabs.aggregation"), path: "/orgs/$org/server/aggregation" },
    { label: t("tabs.activation", "Activation"), path: "/orgs/$org/server/activation" },
    { label: t("tabs.analytics"), path: "/orgs/$org/server/analytics" },
  ];

  if (isLoading) {
    return null;
  }

  if (!user?.isSuperAdmin) {
    navigate({ to: "/orgs/$org", params: { org }, replace: true });
    return null;
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">{t("title")}</h1>
        <p className="text-muted-foreground">{t("subtitle")}</p>
      </div>
      <TabNav tabs={tabs} org={org} />
      <Outlet />
    </div>
  );
}
