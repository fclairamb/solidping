import { useTranslation } from "react-i18next";
import { AlertTriangle } from "lucide-react";

import type { DependencyWarning } from "@/api/hooks";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { resolveCheckRefLabel } from "@/lib/dependency-graph";

interface DependencyWarningsProps {
  warnings?: DependencyWarning[];
}

/**
 * The soft configuration lint on a check's hard `dependsOn` edges
 * (spec 2026-08-31-06).
 *
 * Amber, never destructive, and never a blocking validation: the edge is
 * legal, the runtime confirmation hold already covers the gap at page time,
 * and the only consequence is that a page may arrive later than this check's
 * configured confirmation suggests. Renders nothing when there is nothing to
 * say, so a card can mount it unconditionally.
 */
export function DependencyWarnings({ warnings }: DependencyWarningsProps) {
  const { t } = useTranslation(["dependencies"]);

  if (!warnings || warnings.length === 0) return null;

  return (
    <div className="space-y-2" data-testid="dependency-warnings">
      {warnings.map((warning) => (
        <Alert key={warning.dependencyUid} variant="warning">
          <AlertTriangle />
          <AlertTitle>
            {t("dependencies:warnings.confirmationMargin.title", {
              parent:
                resolveCheckRefLabel(warning.parentCheck) ||
                t("dependencies:unknownCheck"),
            })}
          </AlertTitle>
          <AlertDescription>
            {t("dependencies:warnings.confirmationMargin.body", {
              parent:
                resolveCheckRefLabel(warning.parentCheck) ||
                t("dependencies:unknownCheck"),
              current: warning.childConfirmationSeconds,
              recommended: warning.recommendedConfirmationSeconds,
            })}
          </AlertDescription>
        </Alert>
      ))}
    </div>
  );
}
