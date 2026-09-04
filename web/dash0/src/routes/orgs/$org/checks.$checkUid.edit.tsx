import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
  useCheck,
  useUpdateCheck,
  useCheckGroups,
  useRegions,
  useSetCheckConnections,
  useCreateCheckDependency,
  useUpdateCheckDependency,
  useDeleteCheckDependency,
  useCheckDependencies,
  useDependencyGraph,
} from "@/api/hooks";
import { diffDependencies } from "@/lib/dependency-diff";
import { mapDependencySaveError } from "@/lib/dependency-save-error";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorView } from "@/components/shared/error-views";
import { CheckForm } from "@/components/shared/check-form";

export const Route = createFileRoute("/orgs/$org/checks/$checkUid/edit")({
  // `?section=<name>` deep-link only: expand + scroll that collapsible on load.
  // Unlike /new, the edit form never pre-fills field VALUES from query params —
  // silently mutating an existing check's form state from a URL is unwanted.
  validateSearch: (search: Record<string, unknown>): { section?: string } =>
    typeof search.section === "string" ? { section: search.section } : {},
  component: CheckEditPage,
});

function CheckEditPage() {
  const { t } = useTranslation(["checks", "dependencies"]);
  const navigate = useNavigate();
  const { org, checkUid } = Route.useParams();
  const { section } = Route.useSearch();
  // refetchOnMount "always": the form below seeds its field state ONCE from
  // initialData, so it must never seed from a stale cache entry (e.g.
  // re-opening the editor right after a save, when react-query returns the
  // pre-save snapshot synchronously). Force a fetch on mount and hold the
  // skeleton until it lands (isFetchedAfterMount); once the form is up,
  // later background refetches never unmount it mid-edit because
  // isFetchedAfterMount stays true for the life of the component.
  const {
    data: check,
    isLoading,
    isFetchedAfterMount,
    error,
    refetch,
  } = useCheck(org, checkUid, { refetchOnMount: "always" });
  const updateCheck = useUpdateCheck(org, checkUid);
  const setConnections = useSetCheckConnections(org, checkUid);
  const createDep = useCreateCheckDependency(org, checkUid);
  const updateDep = useUpdateCheckDependency(org, checkUid);
  const deleteDep = useDeleteCheckDependency(org, checkUid);
  const { data: existingDepsForSync } = useCheckDependencies(org, checkUid);
  const { data: dependencyGraph } = useDependencyGraph(org);
  const { data: checkGroups } = useCheckGroups(org);
  const { data: regionsData } = useRegions(org);

  const waitingForFreshData = !error && !isFetchedAfterMount;

  if (isLoading || waitingForFreshData) {
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
      initialSection={section}
      checkGroups={checkGroups}
      availableRegions={regionsData?.regions}
      defaultRegions={regionsData?.defaultRegions}
      isPending={updateCheck.isPending}
      onCancel={() =>
        navigate({
          to: "/orgs/$org/checks/$checkUid",
          params: { org, checkUid: redirectToUid },
          search: {
            graphPeriod: undefined,
            graphFull: undefined,
            region: undefined,
          },
        })
      }
      onSubmit={async (data) => {
        // NOTE: this list must stay in sync with the fields CheckForm's
        // onSubmit builder puts in `data` (check-form.tsx) that also belong
        // in UpdateCheckRequest (hooks.ts) — a field added to the form but
        // missed here is silently dropped before the request is ever sent.
        // `connectionUids`/`dependsOn`/`initialDependsOn` are intentionally
        // excluded: they're not part of UpdateCheckRequest and are applied
        // separately below via setConnections and the dependency mutations.
        await updateCheck.mutateAsync({
          enabled: data.enabled,
          name: data.name,
          slug: data.slug,
          checkGroupUid: data.checkGroupUid,
          escalationPolicyUid: data.escalationPolicyUid,
          period: data.period,
          config: data.config,
          regions: data.regions,
          ...(data.regionSpread !== undefined
            ? { regionSpread: data.regionSpread }
            : {}),
          tracerouteOnFailure: data.tracerouteOnFailure,
          reopenCooldownMultiplier: data.reopenCooldownMultiplier,
          flappingWindowSeconds: data.flappingWindowSeconds,
          flapBackoffFactor: data.flapBackoffFactor,
          maxRecoveryMultiplier: data.maxRecoveryMultiplier,
          confirmationPeriodSeconds: data.confirmationPeriodSeconds,
          recoveryPeriodSeconds: data.recoveryPeriodSeconds,
          ...(data.labels !== undefined ? { labels: data.labels } : {}),
        });
        if (data.connectionUids !== undefined) {
          await setConnections.mutateAsync(data.connectionUids);
        }
        if (data.dependsOn !== undefined) {
          // Three buckets, not two: an edge whose kind or description changed
          // is a PATCH, and used to be silently dropped — the form could only
          // add and remove, so retuning hard -> soft was impossible here.
          const { toAdd, toUpdate, toRemove } = diffDependencies(
            data.dependsOn,
            existingDepsForSync?.dependsOn ?? [],
          );
          for (const draft of toAdd) {
            try {
              await createDep.mutateAsync({
                parentCheckUid: draft.parentCheckUid,
                kind: draft.kind,
                description: draft.description || undefined,
              });
            } catch (err) {
              throw mapDependencySaveError(err, t, {
                graph: dependencyGraph,
                childUid: checkUid,
                parentUid: draft.parentCheckUid,
              });
            }
          }
          for (const edge of toUpdate) {
            try {
              // An empty description is sent as "" on purpose: that is how the
              // API clears one (a dropped field means "leave unchanged").
              await updateDep.mutateAsync({
                uid: edge.uid,
                kind: edge.kind,
                description: edge.description,
              });
            } catch (err) {
              throw mapDependencySaveError(err, t, {
                graph: dependencyGraph,
                childUid: checkUid,
                parentUid: edge.parentCheckUid,
              });
            }
          }
          for (const edge of toRemove) {
            await deleteDep.mutateAsync(edge.uid);
          }
        }
        toast.success(t("toast.updated"));
        navigate({
          to: "/orgs/$org/checks/$checkUid",
          params: { org, checkUid: redirectToUid },
          search: {
            graphPeriod: undefined,
            graphFull: undefined,
            region: undefined,
          },
        });
      }}
    />
  );
}
