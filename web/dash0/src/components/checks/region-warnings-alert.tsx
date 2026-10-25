import { useTranslation } from "react-i18next";
import { AlertTriangle } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";

interface RegionWarningsAlertProps {
  /**
   * The check's `regionWarnings` (detail responses only), recomputed by the
   * server on every read. Undefined or empty when there is nothing to say.
   */
  warnings?: { name: string; message: string; code?: string }[];
}

/**
 * RegionWarningsAlert says why a check does not run in some of its regions
 * right now. Today: a plugin check whose region has no live worker that
 * loaded the plugin (spec 2026-10-17-05 §17). The job waits there until a
 * capable worker shows up, so the operator must deploy the plugin or pick
 * another region.
 *
 * Renders nothing when there is no warning.
 */
export function RegionWarningsAlert({ warnings }: RegionWarningsAlertProps) {
  const { t } = useTranslation(["checks"]);

  if (!warnings || warnings.length === 0) {
    return null;
  }

  return (
    <Alert variant="warning" data-testid="region-warnings">
      <AlertTriangle />
      <AlertTitle>{t("checks:detail.regionWarnings.title")}</AlertTitle>
      <AlertDescription>
        <ul className="space-y-1">
          {warnings.map((warning) => (
            <li key={`${warning.code ?? ""}-${warning.message}`} data-testid="region-warning-item">
              {warning.message}
            </li>
          ))}
        </ul>
      </AlertDescription>
    </Alert>
  );
}
