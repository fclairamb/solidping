import { cn } from "@/lib/utils";

type AuroraPanelProps = React.HTMLAttributes<HTMLDivElement>;

/**
 * Dark, brand-tinted hero backdrop: a deep base with three oversized,
 * heavily-blurred color blobs (an "aurora") plus a soft gradient wash.
 *
 * Always renders dark regardless of the active theme — it is a *marketing*
 * surface (login split-screen, hero strips, empty-state splashes), not
 * operator chrome. The blobs pull from the existing palette (primary blue,
 * brand crimson, chart-5 violet) so the glow stays on-brand without
 * introducing new colors. Children render above the glow on a z-10 layer
 * and inherit white text.
 */
export function AuroraPanel({ className, children, ...props }: AuroraPanelProps) {
  return (
    <div
      className={cn(
        "relative isolate overflow-hidden bg-slate-950 text-white",
        className,
      )}
      {...props}
    >
      {/* Soft directional wash tying the two corner glows together. */}
      <div className="pointer-events-none absolute inset-0 bg-gradient-to-br from-primary/25 via-transparent to-brand/25" />
      {/* Aurora blobs — oversized, off-canvas, heavily blurred. */}
      <div className="pointer-events-none absolute -left-1/4 -top-1/4 h-[70%] w-[70%] rounded-full bg-primary/40 blur-[120px]" />
      <div className="pointer-events-none absolute -bottom-1/4 -right-[10%] h-[65%] w-[65%] rounded-full bg-brand/40 blur-[120px]" />
      <div className="pointer-events-none absolute right-0 top-[15%] h-[45%] w-[45%] rounded-full bg-chart-5/30 blur-[100px]" />
      <div className="relative z-10 flex h-full w-full flex-col">{children}</div>
    </div>
  );
}
