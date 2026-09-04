import { describe, expect, it } from "vitest";

import type { DependencyEdge, DependencyKind } from "@/api/hooks";
import { diffDependencies, type DependencyDraft } from "./dependency-diff";

const child = { uid: "child", slug: "child", name: "Child" };

function edge(
  uid: string,
  parentUid: string,
  kind: DependencyKind = "hard",
  description?: string,
): DependencyEdge {
  return {
    uid,
    parentCheck: { uid: parentUid, slug: parentUid, name: parentUid },
    childCheck: child,
    kind,
    ...(description !== undefined ? { description } : {}),
  };
}

function draft(
  parentCheckUid: string,
  kind: DependencyKind = "hard",
  description = "",
): DependencyDraft {
  return { parentCheckUid, kind, description };
}

describe("diffDependencies", () => {
  it("returns three empty buckets when nothing is staged and nothing exists", () => {
    expect(diffDependencies([], [])).toEqual({
      toAdd: [],
      toUpdate: [],
      toRemove: [],
    });
  });

  it("adds parents that are not linked yet, carrying their staged kind and description", () => {
    const diff = diffDependencies([draft("p1", "soft", "upstream cdn")], []);
    expect(diff.toAdd).toEqual([
      { parentCheckUid: "p1", kind: "soft", description: "upstream cdn" },
    ]);
    expect(diff.toUpdate).toEqual([]);
    expect(diff.toRemove).toEqual([]);
  });

  it("removes edges whose parent is no longer staged", () => {
    const diff = diffDependencies([], [edge("e1", "p1", "hard", "why")]);
    expect(diff.toRemove).toEqual([
      { uid: "e1", parentCheckUid: "p1", kind: "hard", description: "why" },
    ]);
    expect(diff.toAdd).toEqual([]);
    expect(diff.toUpdate).toEqual([]);
  });

  it("updates an existing edge whose kind changed", () => {
    const diff = diffDependencies([draft("p1", "soft")], [edge("e1", "p1", "hard")]);
    expect(diff.toUpdate).toEqual([
      { uid: "e1", parentCheckUid: "p1", kind: "soft", description: "" },
    ]);
    expect(diff.toAdd).toEqual([]);
    expect(diff.toRemove).toEqual([]);
  });

  it("updates an existing edge whose description changed, including clearing it", () => {
    const set = diffDependencies(
      [draft("p1", "hard", "shared database")],
      [edge("e1", "p1", "hard")],
    );
    expect(set.toUpdate).toEqual([
      {
        uid: "e1",
        parentCheckUid: "p1",
        kind: "hard",
        description: "shared database",
      },
    ]);

    const cleared = diffDependencies(
      [draft("p1", "hard", "")],
      [edge("e1", "p1", "hard", "shared database")],
    );
    expect(cleared.toUpdate).toEqual([
      { uid: "e1", parentCheckUid: "p1", kind: "hard", description: "" },
    ]);
  });

  it("issues no write for an untouched edge, whatever shape its empty description has", () => {
    // The API omits `description` entirely when it is null; the form always
    // holds a string. Re-saving must not PATCH.
    expect(
      diffDependencies([draft("p1", "hard", "")], [edge("e1", "p1", "hard")])
        .toUpdate,
    ).toEqual([]);
    expect(
      diffDependencies(
        [draft("p1", "soft", "  keeps trailing space  ")],
        [edge("e1", "p1", "soft", "keeps trailing space")],
      ).toUpdate,
    ).toEqual([]);
  });

  it("splits a mixed change into the right buckets", () => {
    const diff = diffDependencies(
      [
        draft("keep", "hard", "unchanged"),
        draft("retune", "soft", "now informational"),
        draft("fresh", "soft", "brand new"),
      ],
      [
        edge("e-keep", "keep", "hard", "unchanged"),
        edge("e-retune", "retune", "hard"),
        edge("e-drop", "drop", "hard", "gone"),
      ],
    );
    expect(diff.toAdd.map((d) => d.parentCheckUid)).toEqual(["fresh"]);
    expect(diff.toUpdate.map((u) => u.uid)).toEqual(["e-retune"]);
    expect(diff.toRemove.map((u) => u.uid)).toEqual(["e-drop"]);
  });

  it("trims the staged description before writing it", () => {
    const diff = diffDependencies([draft("p1", "hard", "  padded  ")], []);
    expect(diff.toAdd[0].description).toBe("padded");
  });
});
