import { useEffect, useMemo } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
  useCreateStatusPage,
  useCheck,
  useInfiniteChecks,
  type Check,
} from "@/api/hooks";
import { useAuth } from "@/contexts/AuthContext";
import { StatusPageForm } from "@/components/shared/status-page-form";

export const Route = createFileRoute("/orgs/$org/status-pages/new")({
  // Optional check to pre-attach (spec 2026-08-28-16): reached via the
  // check detail page's "Publish on a status page" link. The explicit
  // `checkUid?:` return type (not just `string | undefined`) is what keeps
  // `search` optional on every other `<Link to="...status-pages/new">` in
  // the app — mirrors login.tsx / forgot-password.tsx.
  validateSearch: (search: Record<string, unknown>): { checkUid?: string } => ({
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

  // Full check list, for the "Prefill for me" wand (attach every check) and
  // to resolve attached-check badge names. Same auto-page-through pattern as
  // checks.scheduling.tsx's auto-rebalance — the whole point is the ORG
  // total, so stopping at the list endpoint's first 100-row page would
  // quietly under-attach an org with more checks than that.
  const {
    data: checksPages,
    hasNextPage,
    fetchNextPage,
    isFetchingNextPage,
    isLoading: isLoadingChecks,
    isError: isChecksError,
  } = useInfiniteChecks(org, { limit: 100 });

  useEffect(() => {
    if (hasNextPage && !isFetchingNextPage) {
      void fetchNextPage();
    }
  }, [hasNextPage, isFetchingNextPage, fetchNextPage]);

  useEffect(() => {
    if (isChecksError) {
      toast.error(t("wand.loadChecksFailed", "Couldn't load the full check list"));
    }
  }, [isChecksError, t]);

  const allChecks: Check[] = useMemo(
    () => (checksPages?.pages ?? []).flatMap((page) => page.data ?? []),
    [checksPages],
  );
  // Loaded means "no more pages to fetch" — an error stops the retry loop
  // too, so the wand still becomes usable with whatever was fetched so far
  // rather than spinning forever.
  const allChecksLoaded =
    !isLoadingChecks && (hasNextPage === false || isChecksError);

  const checkNamesByUid = useMemo(() => {
    const map = new Map<string, string>();
    for (const check of allChecks) {
      map.set(check.uid, check.name ?? check.uid);
    }
    if (prefilledCheck.data) {
      map.set(
        prefilledCheck.data.uid,
        prefilledCheck.data.name ?? prefilledCheck.data.uid,
      );
    }
    return map;
  }, [allChecks, prefilledCheck.data]);

  return (
    <StatusPageForm
      mode="create"
      isPending={createStatusPage.isPending}
      initialName={checkUid ? orgName : undefined}
      initialCheckUids={checkUid ? [checkUid] : undefined}
      checkNamesByUid={checkNamesByUid}
      orgName={orgName}
      allChecks={allChecks}
      allChecksLoaded={allChecksLoaded}
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
          checkUids: data.checkUids.length > 0 ? data.checkUids : undefined,
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
