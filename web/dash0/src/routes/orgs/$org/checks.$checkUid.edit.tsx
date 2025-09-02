import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import { useCheck, useUpdateCheck, useCheckGroups, useRegions } from "@/api/hooks";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorView } from "@/components/shared/error-views";
import { CheckForm } from "@/components/shared/check-form";

export const Route = createFileRoute("/orgs/$org/checks/$checkUid/edit")({
  component: CheckEditPage,
});

function CheckEditPage() {
  const navigate = useNavigate();
  const { org, checkUid } = Route.useParams();
  const { data: check, isLoading, error, refetch } = useCheck(org, checkUid);
  const updateCheck = useUpdateCheck(org, checkUid);
  const { data: checkGroups } = useCheckGroups(org);
  const { data: regionsData } = useRegions(org);

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
        resource="Check"
        backTo="/orgs/$org/checks"
        backLabel="Back to Checks"
        onRetry={() => refetch()}
      />
    );
  }

  if (!check) {
    return null;
  }

  // Always redirect to UID-based URL after edit
  const redirectToUid = check.uid;

  return (
    <CheckForm
      org={org}
      mode="edit"
      initialData={check}
      checkGroups={checkGroups}
      availableRegions={regionsData?.regions}
      defaultRegions={regionsData?.defaultRegions}
      isPending={updateCheck.isPending}
      onCancel={() =>
        navigate({
          to: "/orgs/$org/checks/$checkUid",
          params: { org, checkUid: redirectToUid },
          search: { graphPeriod: undefined, graphFull: undefined },
        })
      }
      onSubmit={async (data) => {
        await updateCheck.mutateAsync({
          name: data.name,
          slug: data.slug,
          checkGroupUid: data.checkGroupUid,
          period: data.period,
          config: data.config,
          regions: data.regions,
          reopenCooldownMultiplier: data.reopenCooldownMultiplier,
          maxAdaptiveIncrease: data.maxAdaptiveIncrease,
        });
        toast.success("Check updated successfully");
        navigate({
          to: "/orgs/$org/checks/$checkUid",
          params: { org, checkUid: redirectToUid },
          search: { graphPeriod: undefined, graphFull: undefined },
        });
      }}
    />
  );
}
