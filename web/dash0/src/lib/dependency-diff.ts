import type { DependencyEdge, DependencyKind } from "@/api/hooks";

// A staged "depends on" edge as the check form holds it: the parent's uid plus
// the kind/description the user wants that edge to carry once the form is
// saved. The form stages these; nothing is written until Save.
export interface DependencyDraft {
  parentCheckUid: string;
  kind: DependencyKind;
  /** "" means "no description" — the API clears the column on an empty string. */
  description: string;
}

export interface DependencyUpdate {
  /** The existing edge's uid — what PATCH/DELETE address. */
  uid: string;
  parentCheckUid: string;
  kind: DependencyKind;
  description: string;
}

export interface DependencyDiff {
  toAdd: DependencyDraft[];
  toUpdate: DependencyUpdate[];
  toRemove: DependencyUpdate[];
}

function normalizeDescription(value: string | undefined | null): string {
  return (value ?? "").trim();
}

// diffDependencies works out the writes needed to turn `current` (the edges
// the API reports for this check today) into `desired` (what the form has
// staged). Three buckets, because the API has three verbs:
//
//   - toAdd    → POST   (a parent that isn't linked yet)
//   - toUpdate → PATCH  (a parent that is linked, but with a different kind
//                        or description — this bucket is the whole reason the
//                        edit page could previously only add and remove)
//   - toRemove → DELETE (a linked parent the form no longer lists)
//
// Descriptions are compared trimmed, with undefined/null treated as "", so
// re-saving an untouched form issues no PATCH at all.
export function diffDependencies(
  desired: DependencyDraft[],
  current: DependencyEdge[],
): DependencyDiff {
  const currentByParent = new Map<string, DependencyEdge>();
  for (const edge of current) {
    currentByParent.set(edge.parentCheck.uid, edge);
  }
  const desiredParents = new Set(desired.map((d) => d.parentCheckUid));

  const toAdd: DependencyDraft[] = [];
  const toUpdate: DependencyUpdate[] = [];

  for (const draft of desired) {
    const description = normalizeDescription(draft.description);
    const existing = currentByParent.get(draft.parentCheckUid);
    if (!existing) {
      toAdd.push({ ...draft, description });
      continue;
    }
    const unchanged =
      existing.kind === draft.kind &&
      normalizeDescription(existing.description) === description;
    if (unchanged) continue;
    toUpdate.push({
      uid: existing.uid,
      parentCheckUid: draft.parentCheckUid,
      kind: draft.kind,
      description,
    });
  }

  const toRemove: DependencyUpdate[] = current
    .filter((edge) => !desiredParents.has(edge.parentCheck.uid))
    .map((edge) => ({
      uid: edge.uid,
      parentCheckUid: edge.parentCheck.uid,
      kind: edge.kind,
      description: normalizeDescription(edge.description),
    }));

  return { toAdd, toUpdate, toRemove };
}
