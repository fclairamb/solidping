import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

import { cn } from "@/lib/utils";

type PageHeaderProps = {
  icon: LucideIcon;
  title: string;
  description?: string;
  actions?: ReactNode;
  className?: string;
  iconClassName?: string;
};

export function PageHeader({
  icon: Icon,
  title,
  description,
  actions,
  className,
  iconClassName,
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
      {actions ? (
        <div className="ml-auto flex shrink-0 items-center gap-2">{actions}</div>
      ) : null}
    </div>
  );
}
