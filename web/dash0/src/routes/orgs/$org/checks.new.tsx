import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { toast } from "sonner";
import { useCreateCheck, useCheckGroups, useRegions } from "@/api/hooks";
import { CheckForm } from "@/components/shared/check-form";
import type { Check } from "@/api/hooks";

function secondsToHMS(seconds: number): string {
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = seconds % 60;
  return `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
}

export const Route = createFileRoute("/orgs/$org/checks/new")({
  validateSearch: (search: Record<string, unknown>) => ({
    checkType: typeof search.checkType === "string" ? search.checkType : undefined,
    checkPeriod: typeof search.checkPeriod === "string" ? parseInt(search.checkPeriod) : undefined,
    checkName: typeof search.checkName === "string" ? search.checkName : undefined,
    checkSlug: typeof search.checkSlug === "string" ? search.checkSlug : undefined,
    httpUrl: typeof search.httpUrl === "string" ? search.httpUrl : undefined,
    httpMethod: typeof search.httpMethod === "string" ? search.httpMethod : undefined,
    host: typeof search.host === "string" ? search.host : undefined,
    port: typeof search.port === "string" ? parseInt(search.port) : undefined,
    url: typeof search.url === "string" ? search.url : undefined,
    domain: typeof search.domain === "string" ? search.domain : undefined,
    username: typeof search.username === "string" ? search.username : undefined,
    database: typeof search.database === "string" ? search.database : undefined,
  }),
  component: CheckNewPage,
});

function CheckNewPage() {
  const navigate = useNavigate();
  const { org } = Route.useParams();
  const search = Route.useSearch();
  const createCheck = useCreateCheck(org);
  const { data: checkGroups } = useCheckGroups(org);
  const { data: regionsData } = useRegions(org);

  const hasSearchParams = Object.values(search).some((v) => v !== undefined);

  const initialData: Partial<Check> | undefined = hasSearchParams
    ? {
        type: search.checkType as Check["type"] | undefined,
        name: search.checkName,
        slug: search.checkSlug,
        period: search.checkPeriod ? secondsToHMS(search.checkPeriod) : undefined,
        config: {
          ...(search.httpUrl && { url: search.httpUrl }),
          ...(search.httpMethod && { method: search.httpMethod }),
          ...(search.host && { host: search.host }),
          ...(search.port && { port: search.port }),
          ...(search.url && { url: search.url }),
          ...(search.domain && { domain: search.domain }),
          ...(search.username && { username: search.username }),
          ...(search.database && { database: search.database }),
        },
      }
    : undefined;

  return (
    <CheckForm
      org={org}
      mode="create"
      initialData={initialData as Check | undefined}
      checkGroups={checkGroups}
      availableRegions={regionsData?.regions}
      defaultRegions={regionsData?.defaultRegions}
      isPending={createCheck.isPending}
      onCancel={() => navigate({ to: "/orgs/$org/checks", params: { org } })}
      onSubmit={async (data) => {
        const check = await createCheck.mutateAsync({
          type: data.type,
          name: data.name,
          slug: data.slug,
          checkGroupUid: data.checkGroupUid,
          period: data.period,
          config: data.config ?? {},
          regions: data.regions,
        });
        toast.success("Check created successfully");
        navigate({
          to: "/orgs/$org/checks/$checkUid",
          params: { org, checkUid: check.uid },
          search: { graphPeriod: undefined, graphFull: undefined },
        });
      }}
    />
  );
}
