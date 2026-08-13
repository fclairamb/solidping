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
  /** Same-origin relative docs path (e.g. "/docs/features/check-types"). Renders a small DocsLink next to actions. Only pass this when a genuinely relevant docs page exists. */
  docsHref?: string;
};

export function PageHeader({
  icon: Icon,
  title,
  description,
  actions,
  className,
  iconClassName,
  docsHref,
}: PageHeaderProps) {
  return (
    <div className={cn("flex items-start gap-3", className)}>
      <div
        className={cn(
          "flex h-10 w-10 shrink-0 items-center justify-center rounded-md bg-muted text-foreground",
          iconClassName,
        )}
      >
        <Icon className="h-5 w-5" />
      </div>
      <div className="min-w-0 flex-1">
        <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
        {description ? (
          <p className="mt-1 text-sm text-muted-foreground">{description}</p>
        ) : null}
      </div>
      {actions || docsHref ? (
        <div className="ml-auto flex shrink-0 items-center gap-2">
          {actions}
          {docsHref ? <DocsLink href={docsHref} /> : null}
        </div>
      ) : null}
    </div>
  );
}
