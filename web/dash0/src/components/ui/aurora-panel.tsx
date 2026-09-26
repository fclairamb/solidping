import { cn } from "@/lib/utils";

type AuroraPanelProps = React.HTMLAttributes<HTMLDivElement>;

/**
 * Dark, brand-tinted hero backdrop: the sidebar navy with three oversized,
 * heavily-blurred color blobs (an "aurora") plus a soft gradient wash.
 *
 * Always renders dark regardless of the active theme — it is a *marketing*
 * surface (login split-screen, 404, the no-org welcome), not operator chrome.
 * The base is the sidebar's own navy gradient (`bg-sidebar-gradient`), so the
 * login panel and the sidebar a user sees right after signing in are the same
 * surface. The glow is the electric identity only (spec 2026-09-24-03): a
 * cyan blob (`--aurora-cyan`), a primary blue one and an indigo-violet
 * `chart-5` one, over a cyan-to-indigo wash. No crimson: the logo is the only
 * warm spot on the panel. Children render above the glow on a z-10 layer and
 * inherit white text.
 */
export function AuroraPanel({ className, children, ...props }: AuroraPanelProps) {
  return (
    <div
      data-slot="aurora-panel"
      className={cn(
        "relative isolate overflow-hidden bg-sidebar bg-sidebar-gradient text-white",
        className,
      )}
      {...props}
    >
      {/* Soft directional wash tying the two corner glows together. */}
      <div className="pointer-events-none absolute inset-0 bg-gradient-to-br from-aurora-cyan/25 via-transparent to-chart-5/25" />
      {/* Aurora blobs — oversized, off-canvas, heavily blurred. */}
      <div className="pointer-events-none absolute -left-1/4 -top-1/4 h-[70%] w-[70%] rounded-full bg-aurora-cyan/40 blur-[120px]" />
      <div className="pointer-events-none absolute -bottom-1/4 -right-[10%] h-[65%] w-[65%] rounded-full bg-primary/40 blur-[120px]" />
      <div className="pointer-events-none absolute right-0 top-[15%] h-[45%] w-[45%] rounded-full bg-chart-5/30 blur-[100px]" />
      <div className="relative z-10 flex h-full w-full flex-col">{children}</div>
    </div>
  );
}
