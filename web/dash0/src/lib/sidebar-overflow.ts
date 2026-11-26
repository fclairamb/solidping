export type SidebarOverflow = "none" | "top" | "bottom" | "both";

// Which edges of the sidebar's scroll box hide menu items. A 1px tolerance
// absorbs fractional scroll positions on scaled displays.
export function sidebarOverflow(
  scrollTop: number,
  scrollHeight: number,
  clientHeight: number,
): SidebarOverflow {
  const above = scrollTop > 1;
  const below = scrollHeight - scrollTop - clientHeight > 1;
  if (above && below) return "both";
  if (above) return "top";
  if (below) return "bottom";
  return "none";
}
