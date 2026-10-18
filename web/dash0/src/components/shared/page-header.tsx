import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

import { DocsLink } from "@/components/shared/docs-link";
import { cn } from "@/lib/utils";

type PageHeaderProps = {
  icon: LucideIcon;
  title: string;
  description?: ReactNode;
  actions?: ReactNode;
  className?: string;
  iconClassName?: string;
  /**
   * The icon tile's look. "brand" (default) is the primary gradient with a
   * white icon, no glow. "neutral" is the flat bg-muted tile, for third-party
   * logos (an integration's provider mark) that must keep their own colors.
   */
  tone?: "brand" | "neutral";
  /** Same-origin relative docs path (e.g. "/docs/features/check-types"). Renders a small DocsLink next to actions. Only pass this when a genuinely relevant docs page exists. */
  docsHref?: string;
};

const TILE_TONE: Record<NonNullable<PageHeaderProps["tone"]>, string> = {
  brand: "bg-primary-fill bg-primary-gradient text-primary-fill-foreground shadow-xs",
  neutral: "bg-muted text-foreground",
};

export function PageHeader({
  icon: Icon,
  title,
  description,
  actions,
  className,
  iconClassName,
  tone = "brand",
  docsHref,
}: PageHeaderProps) {
  return (
    // flex-wrap: on a phone the title block claims the whole row next to the
    // tile, so the actions (a primary button plus extras) always drop below
    // it instead of crushing the title to one word per line. From sm up they
    // share the row, pushed right.
    <div className={cn("flex flex-wrap items-start gap-3", className)}>
      <div
        data-slot="page-header-tile"
        data-tone={tone}
        className={cn(
          "flex h-9 w-9 shrink-0 items-center justify-center rounded-lg",
          TILE_TONE[tone],
          iconClassName,
        )}
      >
        <Icon className="h-[18px] w-[18px]" />
      </div>
      <div className="min-w-0 flex-1 basis-[calc(100%-3rem)] sm:basis-40">
        <h1 className="text-xl font-semibold tracking-tight sm:text-2xl">{title}</h1>
        {description ? (
          <p className="mt-0.5 text-sm text-muted-foreground">{description}</p>
        ) : null}
      </div>
      {actions || docsHref ? (
        <div
          data-slot="page-header-actions"
          className="flex max-w-full shrink-0 flex-wrap items-center gap-2 sm:ml-auto"
        >
          {actions}
          {docsHref ? <DocsLink href={docsHref} /> : null}
        </div>
      ) : null}
    </div>
  );
}
