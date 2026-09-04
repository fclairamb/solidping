import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Trash2 } from "lucide-react";

import {
  useDependencyGraph,
  type DependencyKind,
  type GraphResponse,
} from "@/api/hooks";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { CheckPicker } from "@/components/shared/check-picker";
import { formatCyclePath } from "@/components/shared/dependency-cycle-path";
import {
  DependencyEmptyRow,
  DependencyRow,
  DependencyRowList,
} from "@/components/checks/dependency-row";
import { ancestorsAndDescendants } from "@/lib/dependency-graph";

/** One staged parent edge, as the check form holds it before Save. */
export interface DependencyParentDraft {
  uid: string;
  label: string;
  kind: DependencyKind;
  description: string;
}

interface DependsOnFormSectionProps {
  org: string;
  checkUid: string | undefined;
  parents: DependencyParentDraft[];
  onAdd: (parent: DependencyParentDraft) => void;
  onRemove: (uid: string) => void;
  onChange: (
    uid: string,
    patch: Partial<Pick<DependencyParentDraft, "kind" | "description">>,
  ) => void;
}

// DependsOnFormSection is the body of the check form's Dependencies
// collapsible, and the ONLY place a dependency is created, retuned or removed
// — the check detail page's card is read-only. Everything here is staged in
// form state and written when the form is saved.
export function DependsOnFormSection({
  org,
  checkUid,
  parents,
  onAdd,
  onRemove,
  onChange,
}: DependsOnFormSectionProps) {
  const { t } = useTranslation(["dependencies", "common"]);
  const { data: graph } = useDependencyGraph(org);

  // Cycle prevention, layer one: never offer a check that would close a loop.
  // The picker excludes this check, its descendants (a descendant becoming a
  // parent IS the cycle) and parents already staged. Layer two is the
  // DEPENDENCY_CYCLE mapping on save below — the graph this was computed
  // against can be stale by the time the form is submitted.
  const excludeUids = useMemo(() => {
    const set = new Set<string>(parents.map((p) => p.uid));
    if (checkUid) {
      set.add(checkUid);
      if (graph) {
        const { descendants } = ancestorsAndDescendants(graph, checkUid);
        for (const uid of descendants) set.add(uid);
      }
    }
    return set;
  }, [parents, checkUid, graph]);

  return (
    <div className="space-y-2">
      <Label>{t("dependencies:dependsOn")}</Label>
      <p className="text-xs text-muted-foreground">
        {t("dependencies:dependsOnHelp")} — {t("dependencies:kindHard")}:{" "}
        {t("dependencies:kindHardTooltip")}. {t("dependencies:kindSoft")}:{" "}
        {t("dependencies:kindSoftTooltip")}.
      </p>
      <DependencyRowList tone="card" data-testid="dependency-editor">
        {parents.map((parent) => (
          <DependencyRow
            key={parent.uid}
            data-testid={`dependency-editor-row-${parent.uid}`}
            identity={parent.label}
            kind={
              <Select
                value={parent.kind}
                onValueChange={(v) =>
                  onChange(parent.uid, { kind: v as DependencyKind })
                }
              >
                <SelectTrigger
                  className="h-10 w-full sm:w-28"
                  aria-label={t("dependencies:kind")}
                  data-testid={`dependency-kind-select-${parent.uid}`}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="hard">
                    {t("dependencies:kindHard")}
                  </SelectItem>
                  <SelectItem value="soft">
                    {t("dependencies:kindSoft")}
                  </SelectItem>
                </SelectContent>
              </Select>
            }
            description={
              <Input
                value={parent.description}
                onChange={(e) =>
                  onChange(parent.uid, { description: e.target.value })
                }
                placeholder={t("dependencies:descriptionPlaceholder")}
                aria-label={t("dependencies:description")}
                data-testid={`dependency-description-input-${parent.uid}`}
                className="h-10"
              />
            }
            actions={
              <Button
                type="button"
                size="icon"
                variant="ghost"
                className="h-10 w-10 text-destructive hover:text-destructive"
                onClick={() => onRemove(parent.uid)}
                aria-label={t("dependencies:remove")}
                data-testid={`dependency-remove-${parent.uid}`}
              >
                <Trash2 className="h-3.5 w-3.5" />
              </Button>
            }
          />
        ))}
        {parents.length === 0 && (
          <DependencyEmptyRow>{t("dependencies:noParents")}</DependencyEmptyRow>
        )}
        <AddDependencyRow
          org={org}
          checkUid={checkUid}
          excludeUids={excludeUids}
          graph={graph}
          onAdd={onAdd}
        />
      </DependencyRowList>
    </div>
  );
}

interface AddDependencyRowProps {
  org: string;
  checkUid: string | undefined;
  excludeUids: Set<string>;
  graph?: GraphResponse;
  onAdd: (parent: DependencyParentDraft) => void;
}

// The add row lives inside the same bordered container as the staged rows,
// separated by a dashed rule rather than floating below as a second box.
function AddDependencyRow({
  org,
  checkUid,
  excludeUids,
  graph,
  onAdd,
}: AddDependencyRowProps) {
  const { t } = useTranslation(["dependencies", "common"]);
  const [parentUid, setParentUid] = useState<string | undefined>(undefined);
  const [parentLabel, setParentLabel] = useState<string | undefined>(undefined);
  const [kind, setKind] = useState<DependencyKind>("hard");
  const [description, setDescription] = useState("");
  const [error, setError] = useState<string | null>(null);

  const handleAdd = () => {
    if (!parentUid) return;
    // Layer-two cycle guard against a graph the picker may not have reflected
    // yet: if the picked check is reachable FROM this check, the new edge
    // closes a loop. The server rejects it too (DEPENDENCY_CYCLE) — this just
    // says so before the user fills in the rest of the form.
    if (checkUid && graph) {
      const { descendants } = ancestorsAndDescendants(graph, checkUid);
      if (descendants.has(parentUid) || parentUid === checkUid) {
        setError(
          t("dependencies:errors.cycle", {
            path: formatCyclePath(graph, checkUid, parentUid),
          }),
        );
        return;
      }
    }
    onAdd({
      uid: parentUid,
      label: parentLabel || parentUid,
      kind,
      description,
    });
    setParentUid(undefined);
    setParentLabel(undefined);
    setKind("hard");
    setDescription("");
    setError(null);
  };

  return (
    <div className="border-dashed">
      <DependencyRow
        data-testid="dependency-add-row"
        identity={
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
        }
        kind={
          <Select
            value={kind}
            onValueChange={(v) => setKind(v as DependencyKind)}
          >
            <SelectTrigger
              className="h-10 w-full sm:w-28"
              aria-label={t("dependencies:kind")}
              data-testid="dependency-add-kind-select"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="hard">{t("dependencies:kindHard")}</SelectItem>
              <SelectItem value="soft">{t("dependencies:kindSoft")}</SelectItem>
            </SelectContent>
          </Select>
        }
        description={
          <Input
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            placeholder={t("dependencies:descriptionPlaceholder")}
            aria-label={t("dependencies:description")}
            data-testid="dependency-add-description-input"
            className="h-10"
          />
        }
        actions={
          <Button
            type="button"
            size="sm"
            className="h-10"
            onClick={handleAdd}
            disabled={!parentUid}
            data-testid="dependency-add-button"
          >
            {t("dependencies:addDependency")}
          </Button>
        }
      />
      {error && (
        <p
          className="px-3 pb-2 text-xs text-destructive"
          data-testid="dependency-add-error"
        >
          {error}
        </p>
      )}
    </div>
  );
}
