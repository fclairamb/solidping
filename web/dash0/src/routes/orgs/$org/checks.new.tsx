import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useIsDemoSession } from "@/hooks/use-is-demo-session";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
  useCreateCheck,
  useCheckGroups,
  useRegions,
  useDependencyGraph,
} from "@/api/hooks";
import { apiFetch } from "@/api/client";
import { mapDependencySaveError } from "@/lib/dependency-save-error";
import { CheckForm } from "@/components/shared/check-form";
import type { Check } from "@/api/hooks";

function secondsToHMS(seconds: number): string {
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = seconds % 60;
  return `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
}

// Multi-value query params use the singular form, comma-separated, and may also
// repeat (e.g. `?region=us1,eu2` or `?label=env:prod&label=team:infra`). Flatten
// a raw search value (string | string[] | undefined) into a clean string list.
function toList(value: unknown): string[] {
  const raw = Array.isArray(value) ? value : value === undefined ? [] : [value];
  return raw
    .flatMap((v) => {
      if (typeof v === "string") return v.split(",");
      if (typeof v === "number") return [String(v)];
      return [];
    })
    .map((s) => s.trim())
    .filter(Boolean);
}

function listOrUndef(value: unknown): string[] | undefined {
  const list = toList(value);
  return list.length > 0 ? list : undefined;
}

// TanStack Router's default search parser turns bare numeric query values into
// JS numbers (e.g. `?timeout=12` → 12), while non-numeric ones stay strings, so
// accept both.
function toInt(value: unknown): number | undefined {
  if (typeof value === "number") {
    return Number.isFinite(value) ? Math.trunc(value) : undefined;
  }
  if (typeof value !== "string") return undefined;
  const n = parseInt(value, 10);
  return Number.isNaN(n) ? undefined : n;
}

// Parse repeatable `label=key:value` params into a labels record.
function parseLabels(value: unknown): Record<string, string> | undefined {
  const entries = toList(value)
    .map((pair) => {
      const idx = pair.indexOf(":");
      if (idx <= 0) return null;
      return [pair.slice(0, idx).trim(), pair.slice(idx + 1).trim()] as const;
    })
    .filter((e): e is readonly [string, string] => e !== null && e[0] !== "");
  if (entries.length === 0) return undefined;
  return Object.fromEntries(entries);
}

export const Route = createFileRoute("/orgs/$org/checks/new")({
  validateSearch: (search: Record<string, unknown>) => ({
    checkType:
      typeof search.checkType === "string" ? search.checkType : undefined,
    checkPeriod:
      typeof search.checkPeriod === "string"
        ? parseInt(search.checkPeriod)
        : undefined,
    checkName:
      typeof search.checkName === "string" ? search.checkName : undefined,
    checkSlug:
      typeof search.checkSlug === "string" ? search.checkSlug : undefined,
    httpUrl: typeof search.httpUrl === "string" ? search.httpUrl : undefined,
    httpMethod:
      typeof search.httpMethod === "string" ? search.httpMethod : undefined,
    host: typeof search.host === "string" ? search.host : undefined,
    port: typeof search.port === "string" ? parseInt(search.port) : undefined,
    url: typeof search.url === "string" ? search.url : undefined,
    domain: typeof search.domain === "string" ? search.domain : undefined,
    username: typeof search.username === "string" ? search.username : undefined,
    database: typeof search.database === "string" ? search.database : undefined,
    expectedStatus: toInt(search.expectedStatus),
    timeout: toInt(search.timeout),
    // Labels: `?label=key:value`, repeatable and/or comma-separated. Kept as a
    // raw string list so the value round-trips through the URL idempotently;
    // it is parsed into a record in the component.
    label: listOrUndef(search.label),
    // Regions: `?region=us1,eu2` (singular, comma-separated, repeatable).
    region: listOrUndef(search.region),
    // Group slug — resolved to its uid in the component (needs the group list).
    group: typeof search.group === "string" ? search.group : undefined,
    confirmationPeriod: toInt(search.confirmationPeriod),
    recoveryPeriod: toInt(search.recoveryPeriod),
    // `?section=<name>` deep-link: expand + scroll that collapsible on load.
    section: typeof search.section === "string" ? search.section : undefined,
  }),
  component: CheckNewPage,
});

function CheckNewPage() {
  const { t } = useTranslation(["checks", "dependencies"]);
  const { t: tOrg } = useTranslation(["org"]);
  const isDemoSession = useIsDemoSession();
  const navigate = useNavigate();
  const { org } = Route.useParams();
  const search = Route.useSearch();
  const createCheck = useCreateCheck(org);
  const { data: checkGroups } = useCheckGroups(org);
  const { data: regionsData } = useRegions(org);
  const { data: dependencyGraph } = useDependencyGraph(org);

  const hasSearchParams = Object.values(search).some((v) => v !== undefined);

  // A `?group=<slug>` param carries a group slug; the form's group picker keys
  // off the uid, so resolve it here (needs the loaded group list).
  const groupUid = search.group
    ? checkGroups?.find((g) => g.slug === search.group)?.uid
    : undefined;

  const initialData: Partial<Check> | undefined = hasSearchParams
    ? {
        type: search.checkType as Check["type"] | undefined,
        name: search.checkName,
        slug: search.checkSlug,
        period: search.checkPeriod
          ? secondsToHMS(search.checkPeriod)
          : undefined,
        checkGroupUid: groupUid,
        regions: search.region,
        labels: parseLabels(search.label),
        confirmationPeriodSeconds: search.confirmationPeriod,
        recoveryPeriodSeconds: search.recoveryPeriod,
        config: {
          ...(search.httpUrl && { url: search.httpUrl }),
          ...(search.httpMethod && { method: search.httpMethod }),
          ...(search.host && { host: search.host }),
          ...(search.port && { port: search.port }),
          ...(search.url && { url: search.url }),
          ...(search.domain && { domain: search.domain }),
          ...(search.username && { username: search.username }),
          ...(search.database && { database: search.database }),
          ...(search.expectedStatus !== undefined && {
            expectedStatus: search.expectedStatus,
          }),
          ...(search.timeout !== undefined && {
            timeout: `${search.timeout}s`,
          }),
        },
      }
    : undefined;

  return (
    <CheckForm
      org={org}
      mode="create"
      initialData={initialData as Check | undefined}
      initialSection={search.section}
      checkGroups={checkGroups}
      availableRegions={regionsData?.regions}
      defaultRegions={regionsData?.defaultRegions}
      isPending={createCheck.isPending}
      onCancel={() => navigate({ to: "/orgs/$org/checks", params: { org } })}
      onTypeChange={(newType) => {
        navigate({
          to: "/orgs/$org/checks/new",
          params: { org },
          search: {
            ...search,
            checkType: newType,
          },
          replace: true,
        });
      }}
      onSubmit={async (data) => {
        // NOTE: this list must stay in sync with the fields CheckForm's
        // onSubmit builder puts in `data` (check-form.tsx) that also belong
        // in CreateCheckRequest (hooks.ts) — a field added to the form but
        // missed here is silently dropped before the request is ever sent
        // (see the identical warning in checks.$checkUid.edit.tsx, and spec
        // 2026-07-15-04's confirmation/recovery period regression).
        const check = await createCheck.mutateAsync({
          type: data.type,
          enabled: data.enabled,
          name: data.name,
          slug: data.slug,
          checkGroupUid: data.checkGroupUid,
          period: data.period,
          config: data.config ?? {},
          regions: data.regions,
          ...(data.regionSpread !== undefined
            ? { regionSpread: data.regionSpread }
            : {}),
          ...(data.escalationPolicyUid
            ? { escalationPolicyUid: data.escalationPolicyUid }
            : {}),
          ...(data.tracerouteOnFailure
            ? { tracerouteOnFailure: data.tracerouteOnFailure }
            : {}),
          ...(data.labels !== undefined ? { labels: data.labels } : {}),
        });
        if (data.connectionUids && data.connectionUids.length > 0) {
          await apiFetch(`/api/v1/orgs/${org}/checks/${check.uid}/channels`, {
            method: "PUT",
            body: JSON.stringify({ connectionUids: data.connectionUids }),
          });
        }
        if (data.dependsOn && data.dependsOn.length > 0) {
          // Post the kind and description the form staged. This used to
          // hard-code `kind: "hard"`, so a soft edge picked on the create page
          // was silently created as a hard one.
          for (const draft of data.dependsOn) {
            try {
              await apiFetch(
                `/api/v1/orgs/${org}/checks/${check.uid}/dependencies`,
                {
                  method: "POST",
                  body: JSON.stringify({
                    parentCheckUid: draft.parentCheckUid,
                    kind: draft.kind,
                    ...(draft.description
                      ? { description: draft.description }
                      : {}),
                  }),
                },
              );
            } catch (err) {
              throw mapDependencySaveError(err, t, {
                graph: dependencyGraph,
                childUid: check.uid,
                parentUid: draft.parentCheckUid,
              });
            }
          }
        }
        // The deletion IS the conversion hook (spec 2026-09-06-02): a visitor
        // who just watched results arrive from three continents is the moment
        // to say "this disappears in an hour, keep it".
        toast.success(t("toast.created"));

        if (isDemoSession) {
          toast.info(tOrg("org:demo.checkExpires"), { duration: 10000 });
        }
        navigate({
          to: "/orgs/$org/checks/$checkUid",
          params: { org, checkUid: check.uid },
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
