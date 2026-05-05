import { findCyclePath } from "@/lib/dependency-graph";
import type { GraphResponse } from "@/api/hooks";

// Resolves the would-be cycle path from a graph and joins it with " → " for
// inline error display. Returns an empty string when the graph isn't loaded
// yet — callers fall back to a generic "would create a cycle" message.
export function formatCyclePath(
  graph: GraphResponse | undefined,
  childUid: string,
  parentUid: string,
): string {
  if (!graph) return "";
  const path = findCyclePath(graph, childUid, parentUid);
  return path ? path.join(" → ") : "";
}

interface DependencyCyclePathProps {
  graph: GraphResponse | undefined;
  childUid: string;
  parentUid: string;
}

// Renders just the resolved path; hidden when the graph isn't available.
export function DependencyCyclePath({
  graph,
  childUid,
  parentUid,
}: DependencyCyclePathProps) {
  const path = formatCyclePath(graph, childUid, parentUid);
  if (!path) return null;
  return <span className="font-mono">{path}</span>;
}
