// Whether a check form offers the Unix socket target (spec 2026-11-24-01).
//
// ONE decision for every form that can target a socket (tcp today, http,
// redis, postgres… in the later specs): never a hard-coded type list — the
// type's own `supportsLocalSocket` flag says whether its config takes a socket,
// the same rule as tunnel-select.tsx.
//
// The target is offered when the type supports it AND either this server's own
// workers may dial a socket (`localSocketOnServer`: an operator allowlist is
// set, not SaaS) or the check is pinned to private locations only, where the
// agent's own allowlist decides.
import type { CheckTypeInfo } from "@/api/hooks";
import { useCheckFormFields } from "./types/context";

/** Reports whether every selected region is a private location (`@slug`). */
export function pinnedToPrivateOnly(
  placement: string | undefined,
  regions: readonly string[],
): boolean {
  if (placement === "auto") return false;
  return regions.length > 0 && regions.every((slug) => slug.startsWith("@"));
}

/** The pure decision behind useLocalSocketOffered. */
export function isLocalSocketOffered(
  info: Pick<CheckTypeInfo, "supportsLocalSocket" | "localSocketOnServer"> | undefined,
  privateOnly: boolean,
): boolean {
  if (info?.supportsLocalSocket !== true) return false;
  return info.localSocketOnServer === true || privateOnly;
}

/**
 * Whether the active check form offers a Unix socket target. A form that
 * already stores a socket path must show it regardless (it would otherwise
 * hide the stored value): callers OR this with "the state holds a socket".
 */
export function useLocalSocketOffered(): boolean {
  const { checkTypeInfo, privateRegionsOnly } = useCheckFormFields();
  return isLocalSocketOffered(checkTypeInfo, privateRegionsOnly === true);
}
