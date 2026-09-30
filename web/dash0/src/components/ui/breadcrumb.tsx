import * as React from "react";
import { ChevronRight } from "lucide-react";

import { cn } from "@/lib/utils";

/**
 * Breadcrumb trail for detail pages: a parent link (rendered by the caller, so
 * it can be a router `Link`) followed by the current page name, muted and
 * truncated. Render the parent with `BreadcrumbLink` and the current page with
 * `BreadcrumbPage`.
 */
function Breadcrumb({ className, children, ...props }: React.ComponentProps<"nav">) {
  return (
    <nav className={cn("min-w-0 text-sm", className)} {...props}>
      <ol className="flex min-w-0 items-center gap-1.5 text-muted-foreground">
        {children}
      </ol>
    </nav>
  );
}

function BreadcrumbItem({ className, ...props }: React.ComponentProps<"li">) {
  return <li className={cn("inline-flex min-w-0 items-center gap-1.5", className)} {...props} />;
}

function BreadcrumbSeparator({ className, ...props }: React.ComponentProps<"li">) {
  return (
    <li
      role="presentation"
      aria-hidden="true"
      className={cn("inline-flex shrink-0 [&>svg]:h-3.5 [&>svg]:w-3.5", className)}
      {...props}
    >
      <ChevronRight />
    </li>
  );
}

function BreadcrumbPage({ className, ...props }: React.ComponentProps<"span">) {
  return (
    <span aria-current="page" className={cn("truncate text-foreground/70", className)} {...props} />
  );
}

const breadcrumbLinkClassName =
  "inline-flex shrink-0 items-center gap-1 rounded-sm transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

export {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbSeparator,
  BreadcrumbPage,
  breadcrumbLinkClassName,
};
