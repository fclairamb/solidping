import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Building2 } from "lucide-react";
import { PageHeader } from "@/components/shared/page-header";
import { CreateOrgCard } from "@/components/shared/create-org-card";

export const Route = createFileRoute("/orgs/$org/account/organizations/new")({
  component: NewOrganizationPage,
});

function NewOrganizationPage() {
  const { t } = useTranslation("account");
  const { org } = Route.useParams();
  const navigate = useNavigate();

  return (
    <div className="space-y-6">
      <PageHeader icon={Building2} title={t("organizations.newOrganization")} />
      <div className="max-w-lg">
        <CreateOrgCard
          onCancel={() =>
            navigate({
              to: "/orgs/$org/account/organizations",
              params: { org },
            })
          }
        />
      </div>
    </div>
  );
}
