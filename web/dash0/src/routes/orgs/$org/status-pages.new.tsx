import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { useCreateStatusPage, useCheck } from "@/api/hooks";
import { useAuth } from "@/contexts/AuthContext";
import { StatusPageForm } from "@/components/shared/status-page-form";

export const Route = createFileRoute("/orgs/$org/status-pages/new")({
  // Optional check to pre-attach (spec 2026-08-28-16): reached via the
  // check detail page's "Publish on a status page" link. Undefined when
  // absent so a clean URL stays clean, mirroring incidents.index.tsx.
  validateSearch: (search: Record<string, unknown>) => ({
    checkUid:
      typeof search.checkUid === "string" && search.checkUid
        ? search.checkUid
        : undefined,
  }),
  component: StatusPageNewPage,
});

function StatusPageNewPage() {
  const { t } = useTranslation("statusPages");
  const navigate = useNavigate();
  const { org } = Route.useParams();
  const { checkUid } = Route.useSearch();
  const createStatusPage = useCreateStatusPage(org);
  const { organizations } = useAuth();
  const prefilledCheck = useCheck(org, checkUid ?? "", {});
  const orgName = organizations.find((entry) => entry.slug === org)?.name;

  return (
    <StatusPageForm
      mode="create"
      isPending={createStatusPage.isPending}
      initialName={checkUid ? orgName : undefined}
      prefilledCheckName={checkUid ? prefilledCheck.data?.name : undefined}
      onCancel={() => navigate({ to: "/orgs/$org/status-pages", params: { org } })}
      onSubmit={async (data) => {
        const page = await createStatusPage.mutateAsync({
          name: data.name,
          slug: data.slug,
          description: data.description || undefined,
          visibility: data.visibility,
          isDefault: data.isDefault || undefined,
          showAvailability: data.showAvailability,
          showResponseTime: data.showResponseTime,
          historyPeriod: data.historyPeriod,
          hideBranding: data.hideBranding,
          password: data.password,
          autoPublish: data.autoPublish,
          autoPublishDelaySeconds: data.autoPublishDelaySeconds,
          autoResolve: data.autoResolve,
          checkUids: checkUid ? [checkUid] : undefined,
        });
        toast.success(t("toast.created"));
        navigate({
          to: "/orgs/$org/status-pages/$statusPageUid",
          params: { org, statusPageUid: page.uid },
        });
      }}
    />
  );
}
