import type { GraphResponse } from "@/api/hooks";

export function ancestorsAndDescendants(
  graph: GraphResponse,
  checkUid: string,
): { descendants: Set<string>; parentsAlreadySet: Set<string> } {
  const adjOut = new Map<string, string[]>();
  const adjIn = new Map<string, string[]>();
  for (const e of graph.edges) {
    if (!adjOut.has(e.parentCheckUid)) adjOut.set(e.parentCheckUid, []);
    adjOut.get(e.parentCheckUid)!.push(e.childCheckUid);
    if (!adjIn.has(e.childCheckUid)) adjIn.set(e.childCheckUid, []);
    adjIn.get(e.childCheckUid)!.push(e.parentCheckUid);
  }
  const reach = (start: string, adj: Map<string, string[]>) => {
    const seen = new Set<string>();
    const stack = [start];
    while (stack.length) {
      const cur = stack.pop()!;
      for (const next of adj.get(cur) ?? []) {
        if (!seen.has(next)) {
          seen.add(next);
          stack.push(next);
        }
      }
    }
    return seen;
  };
  return {
    descendants: reach(checkUid, adjOut),
    parentsAlreadySet: new Set(adjIn.get(checkUid) ?? []),
  };
}

// findCyclePath returns the chain of check names that would form a cycle when
// we add an edge from parentUid to childUid. It walks the existing graph from
// parentUid back down to childUid via children edges (which is the cycle path
// before the new edge closes the loop). Returns undefined if no path exists.
export function findCyclePath(
  graph: GraphResponse,
  childUid: string,
  parentUid: string,
): string[] | undefined {
  const adjOut = new Map<string, string[]>();
  for (const e of graph.edges) {
    if (!adjOut.has(e.parentCheckUid)) adjOut.set(e.parentCheckUid, []);
    adjOut.get(e.parentCheckUid)!.push(e.childCheckUid);
  }
  const nameByUid = new Map<string, string>();
  for (const n of graph.nodes) {
    nameByUid.set(n.uid, n.name || n.slug);
  }
  const path: string[] = [];
  const seen = new Set<string>();
  const dfs = (cur: string): boolean => {
    if (seen.has(cur)) return false;
    seen.add(cur);
    path.push(nameByUid.get(cur) ?? cur);
    if (cur === childUid) return true;
    for (const next of adjOut.get(cur) ?? []) {
      if (dfs(next)) return true;
    }
    path.pop();
    return false;
  };
  if (!dfs(parentUid)) return undefined;
  // close the cycle by appending the new child step
  path.push(nameByUid.get(childUid) ?? childUid);
  return path;
}
