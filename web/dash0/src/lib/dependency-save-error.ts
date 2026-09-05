import type { TFunction } from "i18next";

import { ApiError } from "@/api/client";
import type { GraphResponse } from "@/api/hooks";
import { formatCyclePath } from "@/components/shared/dependency-cycle-path";

interface CycleContext {
  graph: GraphResponse | undefined;
  /** The check being edited — the child end of the edge being written. */
  childUid: string | undefined;
  /** The parent end of the edge the failed write was about. */
  parentUid: string;
}

// mapDependencySaveError turns a failed dependency write into an ApiError
// carrying a message a human can act on.
//
// The form's picker already excludes descendants so a cycle can't normally be
// staged, but the graph it excluded against may be stale by the time the form
// is submitted (someone else linked the checks in between). So the save path
// keeps its own mapping — cycle, duplicate, unknown/cross-org check — rather
// than surfacing the raw server string.
//
// Returns the original error untouched when it isn't an ApiError, or when its
// code isn't one of the dependency-specific ones.
export function mapDependencySaveError(
  err: unknown,
  t: TFunction,
  ctx: CycleContext,
): unknown {
  if (!(err instanceof ApiError)) return err;
  switch (err.code) {
    case "DEPENDENCY_CYCLE": {
      const path = ctx.childUid
        ? formatCyclePath(ctx.graph, ctx.childUid, ctx.parentUid)
        : "";
      return new ApiError(
        t("dependencies:errors.cycle", { path }),
        err.code,
        err.detail,
        err.status,
      );
    }
    case "DEPENDENCY_DUPLICATE":
      return new ApiError(
        t("dependencies:errors.duplicate"),
        err.code,
        err.detail,
        err.status,
      );
    case "DEPENDENCY_CROSS_ORG":
    case "CHECK_NOT_FOUND":
      return new ApiError(
        t("dependencies:errors.notFound"),
        err.code,
        err.detail,
        err.status,
      );
    default:
      return err;
  }
}
