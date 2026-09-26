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
   * The icon tile's look. "brand" (default) is the accent-gradient tile with a
   * white icon. "neutral" is the flat bg-muted tile, for third-party logos
   * (an integration's provider mark) that must keep their own colors.
   * Use this rather than an iconClassName background: a flat bg-* override
   * only clears the gradient when it goes through cn().
   */
  tone?: "brand" | "neutral";
  /** Same-origin relative docs path (e.g. "/docs/features/check-types"). Renders a small DocsLink next to actions. Only pass this when a genuinely relevant docs page exists. */
  docsHref?: string;
};

const TILE_TONE: Record<NonNullable<PageHeaderProps["tone"]>, string> = {
  brand: "bg-primary bg-accent-gradient text-gradient-foreground shadow-tile",
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
    <div className={cn("flex items-start gap-3", className)}>
      <div
        data-slot="page-header-tile"
        data-tone={tone}
        className={cn(
          "flex h-10 w-10 shrink-0 items-center justify-center rounded-lg",
          TILE_TONE[tone],
          iconClassName,
        )}
      >
        <Icon className="h-5 w-5" />
      </div>
      <div className="min-w-0 flex-1">
        <h1 className="text-2xl font-bold tracking-[-0.025em]">{title}</h1>
        {description ? (
          <p className="mt-1 text-sm text-muted-foreground">{description}</p>
        ) : null}
      </div>
      {actions || docsHref ? (
        <div className="ml-auto flex max-w-full shrink-0 items-center gap-2">
          {actions}
          {docsHref ? <DocsLink href={docsHref} /> : null}
        </div>
      ) : null}
    </div>
  );
}
