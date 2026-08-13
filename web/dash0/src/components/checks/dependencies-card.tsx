import { useMemo, useState } from "react";
import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Pencil, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { ApiError } from "@/api/client";
import {
  useCheckDependencies,
  useCreateCheckDependency,
  useDeleteCheckDependency,
  useDependencyGraph,
  useUpdateCheckDependency,
  type CheckRef,
  type DependencyEdge,
  type DependencyKind,
  type GraphResponse,
} from "@/api/hooks";
import { CheckPicker } from "@/components/shared/check-picker";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { ancestorsAndDescendants, resolveCheckRefLabel } from "@/lib/dependency-graph";
import { formatCyclePath } from "@/components/shared/dependency-cycle-path";

interface DependenciesCardProps {
  org: string;
  checkUid: string;
}

export function DependenciesCard({ org, checkUid }: DependenciesCardProps) {
  const { t } = useTranslation(["dependencies", "common"]);

  const { data: deps, isLoading } = useCheckDependencies(org, checkUid);
  const { data: graph } = useDependencyGraph(org);

  const dependsOn = deps?.dependsOn ?? [];
  const dependedOnBy = deps?.dependedOnBy ?? [];

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("dependencies:title")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-6">
        <DependsOnSection
          org={org}
          checkUid={checkUid}
          dependsOn={dependsOn}
          graph={graph}
          loading={isLoading}
        />
        <DependedOnBySection org={org} dependedOnBy={dependedOnBy} loading={isLoading} />
      </CardContent>
    </Card>
  );
}

interface DependsOnSectionProps {
  org: string;
  checkUid: string;
  dependsOn: DependencyEdge[];
  graph?: GraphResponse;
  loading: boolean;
}

function DependsOnSection({
  org,
  checkUid,
  dependsOn,
  graph,
  loading,
}: DependsOnSectionProps) {
  const { t } = useTranslation(["dependencies", "common"]);

  const excludeUids = useMemo(() => {
    const set = new Set<string>();
    set.add(checkUid);
    if (graph) {
      const { descendants, parentsAlreadySet } = ancestorsAndDescendants(
        graph,
        checkUid,
      );
      for (const u of descendants) set.add(u);
      for (const u of parentsAlreadySet) set.add(u);
    }
    return set;
  }, [graph, checkUid]);

  return (
    <div className="space-y-2">
      <div>
        <h3 className="text-sm font-medium">{t("dependencies:dependsOn")}</h3>
        <p className="text-xs text-muted-foreground">
          {t("dependencies:dependsOnHelp")}
        </p>
      </div>

      <div className="space-y-2">
        {dependsOn.map((edge) => (
          <DependsOnRow key={edge.uid} org={org} checkUid={checkUid} edge={edge} />
        ))}
        {!loading && dependsOn.length === 0 && (
          <p className="text-sm text-muted-foreground">
            {t("dependencies:noDependencies")}
          </p>
        )}
      </div>

      <AddDependencyRow
        org={org}
        checkUid={checkUid}
        excludeUids={excludeUids}
        graph={graph}
      />
    </div>
  );
}

interface DependsOnRowProps {
  org: string;
  checkUid: string;
  edge: DependencyEdge;
}

function DependsOnRow({ org, checkUid, edge }: DependsOnRowProps) {
  const { t } = useTranslation(["dependencies", "common"]);
  const [editing, setEditing] = useState(false);
  const [kind, setKind] = useState<DependencyKind>(edge.kind);
  const [description, setDescription] = useState(edge.description ?? "");
  const [confirmOpen, setConfirmOpen] = useState(false);

  const updateMutation = useUpdateCheckDependency(org, checkUid);
  const deleteMutation = useDeleteCheckDependency(org, checkUid);

  const handleSave = () => {
    updateMutation.mutate(
      { uid: edge.uid, kind, description: description || undefined },
      {
        onSuccess: () => setEditing(false),
        onError: (err) => {
          toast.error(
            err instanceof ApiError ? err.message : t("dependencies:errors.generic"),
          );
        },
      },
    );
  };

  const handleDelete = () => {
    deleteMutation.mutate(edge.uid, {
      onSuccess: () => setConfirmOpen(false),
      onError: (err) => {
        toast.error(
          err instanceof ApiError ? err.message : t("dependencies:errors.generic"),
        );
      },
    });
  };

  if (editing) {
    return (
      <div className="flex flex-wrap items-center gap-2 rounded-md border p-2">
        <span className="font-medium">
          {resolveCheckRefLabel(edge.parentCheck) || t("dependencies:unknownCheck")}
        </span>
        <Select value={kind} onValueChange={(v) => setKind(v as DependencyKind)}>
          <SelectTrigger className="h-8 w-24">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="hard">{t("dependencies:kindHard")}</SelectItem>
            <SelectItem value="soft">{t("dependencies:kindSoft")}</SelectItem>
          </SelectContent>
        </Select>
        <Input
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder={t("dependencies:descriptionPlaceholder")}
          className="h-8 flex-1"
        />
        <Button size="sm" onClick={handleSave} disabled={updateMutation.isPending}>
          {t("common:save", { defaultValue: "Save" })}
        </Button>
        <Button size="sm" variant="ghost" onClick={() => setEditing(false)}>
          {t("common:cancel", { defaultValue: "Cancel" })}
        </Button>
      </div>
    );
  }

  return (
    <div className="flex flex-wrap items-center gap-2 rounded-md border p-2">
      <DependencyCheckLink org={org} check={edge.parentCheck} />
      <KindBadge kind={edge.kind} />
      {edge.description && (
        <span className="text-sm text-muted-foreground">{edge.description}</span>
      )}
      <div className="ml-auto flex items-center gap-1">
        <Button
          size="sm"
          variant="ghost"
          onClick={() => {
            setKind(edge.kind);
            setDescription(edge.description ?? "");
            setEditing(true);
          }}
          aria-label={t("common:edit", { defaultValue: "Edit" })}
        >
          <Pencil className="h-3.5 w-3.5" />
        </Button>
        <AlertDialog open={confirmOpen} onOpenChange={setConfirmOpen}>
          <AlertDialogTrigger asChild>
            <Button
              size="sm"
              variant="ghost"
              aria-label={t("dependencies:remove")}
            >
              <Trash2 className="h-3.5 w-3.5" />
            </Button>
          </AlertDialogTrigger>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>
                {t("dependencies:removeConfirmTitle", {
                  parent: resolveCheckRefLabel(edge.parentCheck) || t("dependencies:unknownCheck"),
                })}
              </AlertDialogTitle>
              <AlertDialogDescription>
                {t("dependencies:removeConfirmBody")}
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>
                {t("common:cancel", { defaultValue: "Cancel" })}
              </AlertDialogCancel>
              <AlertDialogAction onClick={handleDelete}>
                {t("dependencies:remove")}
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </div>
    </div>
  );
}

interface AddDependencyRowProps {
  org: string;
  checkUid: string;
  excludeUids: Set<string>;
  graph?: GraphResponse;
}

function AddDependencyRow({
  org,
  checkUid,
  excludeUids,
  graph,
}: AddDependencyRowProps) {
  const { t } = useTranslation(["dependencies", "common"]);
  const [parentUid, setParentUid] = useState<string | undefined>(undefined);
  const [parentLabel, setParentLabel] = useState<string | undefined>(undefined);
  const [kind, setKind] = useState<DependencyKind>("hard");
  const [description, setDescription] = useState("");
  const [error, setError] = useState<string | null>(null);

  const createMutation = useCreateCheckDependency(org, checkUid);

  const reset = () => {
    setParentUid(undefined);
    setParentLabel(undefined);
    setKind("hard");
    setDescription("");
    setError(null);
  };

  const handleAdd = () => {
    if (!parentUid) return;
    setError(null);
    createMutation.mutate(
      { parentCheckUid: parentUid, kind, description: description || undefined },
      {
        onSuccess: reset,
        onError: (err) => {
          if (err instanceof ApiError) {
            const code = err.code;
            if (code === "DEPENDENCY_CYCLE") {
              setError(
                t("dependencies:errors.cycle", {
                  path: formatCyclePath(graph, checkUid, parentUid),
                }),
              );
              return;
            }
            if (code === "DEPENDENCY_DUPLICATE") {
              setError(t("dependencies:errors.duplicate"));
              return;
            }
            if (code === "DEPENDENCY_CROSS_ORG" || code === "CHECK_NOT_FOUND") {
              toast.error(t("dependencies:errors.notFound"));
              return;
            }
            toast.error(err.message || t("dependencies:errors.generic"));
            return;
          }
          toast.error(t("dependencies:errors.generic"));
        },
      },
    );
  };

  return (
    <div className="rounded-md border border-dashed p-2">
      <div className="flex flex-wrap items-center gap-2">
        <div className="min-w-[12rem] flex-1">
          <CheckPicker
            org={org}
            value={parentUid}
            selectedLabel={parentLabel}
            excludeUids={excludeUids}
            onChange={(uid, c) => {
              setParentUid(uid);
              setParentLabel(c?.name || c?.slug);
              setError(null);
            }}
            placeholder={t("dependencies:pickCheck")}
          />
        </div>
        <Select value={kind} onValueChange={(v) => setKind(v as DependencyKind)}>
          <SelectTrigger className="h-9 w-24">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="hard">{t("dependencies:kindHard")}</SelectItem>
            <SelectItem value="soft">{t("dependencies:kindSoft")}</SelectItem>
          </SelectContent>
        </Select>
        <Input
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder={t("dependencies:descriptionPlaceholder")}
          className="h-9 flex-1"
        />
        <Button
          size="sm"
          onClick={handleAdd}
          disabled={!parentUid || createMutation.isPending}
        >
          {t("dependencies:addDependency")}
        </Button>
      </div>
      {error && (
        <p className="mt-2 text-xs text-destructive" data-testid="dependency-add-error">
          {error}
        </p>
      )}
    </div>
  );
}

interface DependedOnBySectionProps {
  org: string;
  dependedOnBy: DependencyEdge[];
  loading: boolean;
}

function DependedOnBySection({
  org,
  dependedOnBy,
  loading,
}: DependedOnBySectionProps) {
  const { t } = useTranslation(["dependencies"]);

  return (
    <div className="space-y-2">
      <div>
        <h3 className="text-sm font-medium">{t("dependencies:dependedOnBy")}</h3>
        <p className="text-xs text-muted-foreground">
          {t("dependencies:dependedOnByHelp")}
        </p>
      </div>
      <div className="space-y-2">
        {dependedOnBy.map((edge) => (
          <div
            key={edge.uid}
            className="flex flex-wrap items-center gap-2 rounded-md border p-2"
          >
            <DependencyCheckLink org={org} check={edge.childCheck} />
            <KindBadge kind={edge.kind} />
            {edge.description && (
              <span className="text-sm text-muted-foreground">
                {edge.description}
              </span>
            )}
          </div>
        ))}
        {!loading && dependedOnBy.length === 0 && (
          <p className="text-sm text-muted-foreground">
            {t("dependencies:noDependents")}
          </p>
        )}
      </div>
    </div>
  );
}

// DependencyCheckLink renders a dependency row's check reference as a link to
// its detail page. The backend omits edges whose check ref didn't resolve
// (see checkdependencies.Service.ListForCheck), but this stays defensive: if
// `check.uid` is ever empty (e.g. a legacy row from before that filter
// shipped), render muted placeholder text instead of a link to nowhere, so
// the row never shows up as just a bare kind badge (issue #129).
function DependencyCheckLink({ org, check }: { org: string; check: CheckRef }) {
  const { t } = useTranslation(["dependencies"]);
  const label = resolveCheckRefLabel(check);

  if (!check.uid) {
    return (
      <span className="font-medium text-muted-foreground italic">
        {t("dependencies:unknownCheck")}
      </span>
    );
  }

  return (
    <Link
      to="/orgs/$org/checks/$checkUid"
      params={{ org, checkUid: check.uid }}
      search={{
        graphPeriod: undefined,
        graphFull: undefined,
        region: undefined,
      }}
      className="font-medium hover:underline"
    >
      {label}
    </Link>
  );
}

function KindBadge({ kind }: { kind: DependencyKind }) {
  const { t } = useTranslation(["dependencies"]);
  const className =
    kind === "hard"
      ? "bg-red-500/10 text-red-500"
      : "bg-blue-500/10 text-blue-500";
  return (
    <Badge variant="outline" className={className}>
      {kind === "hard" ? t("dependencies:kindHard") : t("dependencies:kindSoft")}
    </Badge>
  );
}
