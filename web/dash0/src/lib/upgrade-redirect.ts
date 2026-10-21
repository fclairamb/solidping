export type UpgradeView =
  | { kind: "loading" }
  | { kind: "redirect"; url: string }
  | { kind: "unavailable" };

/**
 * What the /orgs/$org/upgrade page shows. The entitlements endpoint only
 * returns `upgradeUrl` (with the signed billing token) to org admins on a
 * deployment that has billing, so its absence means there is nowhere to send
 * the viewer.
 */
export function upgradeView(
  state: { isLoading: boolean; isError: boolean; upgradeUrl?: string },
): UpgradeView {
  if (state.isLoading) return { kind: "loading" };
  if (state.isError || !state.upgradeUrl) return { kind: "unavailable" };
  return { kind: "redirect", url: state.upgradeUrl };
}
