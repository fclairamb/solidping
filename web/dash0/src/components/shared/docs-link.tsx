import { BookOpen } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

type DocsLinkProps = {
  /** Same-origin relative path into the embedded docs site, e.g. "/docs/features/check-types". */
  href: string;
  className?: string;
};

/**
 * A small, discreet link into the embedded docs site (served same-origin at
 * `/docs` on every host). Intended for header-level placement via
 * `PageHeader`'s `docsHref` prop — non-intrusive, icon-only, opt-in per page.
 */
export function DocsLink({ href, className }: DocsLinkProps) {
  const { t } = useTranslation();
  const label = t("docsLink.label", "Documentation");

  return (
    <TooltipProvider>
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            asChild
            variant="ghost"
            size="icon"
            className={cn("h-8 w-8 text-muted-foreground", className)}
          >
            <a href={href} target="_blank" rel="noopener" aria-label={label}>
              <BookOpen className="h-4 w-4" />
            </a>
          </Button>
        </TooltipTrigger>
        <TooltipContent>{label}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}
