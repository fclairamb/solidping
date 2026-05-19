import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import { useStatusUpdate, useUpdateStatusUpdate } from "@/api/hooks";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorView } from "@/components/shared/error-views";
import { StatusUpdateForm } from "@/components/shared/status-update-form";

export const Route = createFileRoute(
  "/orgs/$org/status-updates/$updateUid/edit"
)({
  component: StatusUpdateEditPage,
});

function StatusUpdateEditPage() {
  const { org, updateUid } = Route.useParams();
  const navigate = useNavigate();
  const { data: update, isLoading, error, refetch } = useStatusUpdate(org, updateUid);
  const updateMutation = useUpdateStatusUpdate(org, updateUid);

  if (isLoading) {
    return (
      <div className="container mx-auto p-6 space-y-6 max-w-2xl">
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
      <div className="container mx-auto p-6">
        <QueryErrorView
          error={error}
          org={org}
          resource="Status Update"
          backTo="/orgs/$org/status-updates"
          backLabel="Back to Status Updates"
          onRetry={() => refetch()}
        />
      </div>
    );
  }

  if (!update) return null;

  return (
    <div className="container mx-auto p-6">
      <StatusUpdateForm
        org={org}
        mode="edit"
        initialData={update}
        isPending={updateMutation.isPending}
        onCancel={() =>
          navigate({ to: "/orgs/$org/status-updates", params: { org } })
        }
        onSubmit={async (data) => {
          const publishedAt = data.publishedAt
            ? new Date(data.publishedAt).toISOString()
            : undefined;
          await updateMutation.mutateAsync({
            kind: data.kind,
            title: data.title,
            bodyMarkdown: data.bodyMarkdown,
            linkUrl: data.linkUrl || undefined,
            publishedAt,
          });
          toast.success("Status update saved");
          navigate({ to: "/orgs/$org/status-updates", params: { org } });
        }}
      />
    </div>
  );
}
