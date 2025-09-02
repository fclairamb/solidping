import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import { useStatusPage, useUpdateStatusPage } from "@/api/hooks";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorView } from "@/components/shared/error-views";
import { StatusPageForm } from "@/components/shared/status-page-form";

export const Route = createFileRoute(
  "/orgs/$org/status-pages/$statusPageUid/edit"
)({
  component: StatusPageEditPage,
});

function StatusPageEditPage() {
  const navigate = useNavigate();
  const { org, statusPageUid } = Route.useParams();
  const { data: page, isLoading, error, refetch } = useStatusPage(org, statusPageUid);
  const updateStatusPage = useUpdateStatusPage(org, statusPageUid);

  if (isLoading) {
    return (
      <div className="space-y-6 max-w-2xl">
        <div className="flex items-center gap-4">
          <Skeleton className="h-10 w-10 rounded" />
          <Skeleton className="h-8 w-48" />
        </div>
        <Skeleton className="h-96 rounded-lg" />
      </div>
    );
  }

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource="Status Page"
        backTo="/orgs/$org/status-pages"
        backLabel="Back to Status Pages"
        onRetry={() => refetch()}
      />
    );
  }

  if (!page) return null;

  return (
    <StatusPageForm
      mode="edit"
      initialData={page}
      isPending={updateStatusPage.isPending}
      onCancel={() =>
        navigate({
          to: "/orgs/$org/status-pages/$statusPageUid",
          params: { org, statusPageUid: page.uid },
        })
      }
      onSubmit={async (data) => {
        await updateStatusPage.mutateAsync({
          name: data.name,
          slug: data.slug,
          description: data.description || undefined,
          visibility: data.visibility,
          isDefault: data.isDefault,
          enabled: data.enabled,
        });
        toast.success("Status page updated successfully");
        navigate({
          to: "/orgs/$org/status-pages/$statusPageUid",
          params: { org, statusPageUid: page.uid },
        });
      }}
    />
  );
}
