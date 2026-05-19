import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import { useCreateStatusUpdate } from "@/api/hooks";
import { StatusUpdateForm } from "@/components/shared/status-update-form";

export const Route = createFileRoute("/orgs/$org/status-updates/new")({
  component: StatusUpdateNewPage,
});

function StatusUpdateNewPage() {
  const { org } = Route.useParams();
  const navigate = useNavigate();
  const createMutation = useCreateStatusUpdate(org);

  return (
    <div className="container mx-auto p-6">
      <StatusUpdateForm
        org={org}
        mode="create"
        isPending={createMutation.isPending}
        onCancel={() =>
          navigate({ to: "/orgs/$org/status-updates", params: { org } })
        }
        onSubmit={async (data) => {
          const publishedAt = data.publishedAt
            ? new Date(data.publishedAt).toISOString()
            : undefined;
          await createMutation.mutateAsync({
            statusPageUid: data.statusPageUid,
            kind: data.kind,
            title: data.title,
            bodyMarkdown: data.bodyMarkdown,
            linkUrl: data.linkUrl || undefined,
            publishedAt,
          });
          toast.success("Status update created");
          navigate({ to: "/orgs/$org/status-updates", params: { org } });
        }}
      />
    </div>
  );
}
