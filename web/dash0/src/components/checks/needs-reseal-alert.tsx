import { useTranslation } from "react-i18next";
import { AlertTriangle } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";

interface NeedsResealAlertProps {
  /**
   * The check's `needsReseal` flag (detail responses only). Undefined for
   * checks that target no private location.
   */
  needsReseal?: boolean;
}

/**
 * NeedsResealAlert warns that a check running in a private location has
 * credentials sealed to a different agent set than the one currently active —
 * an agent enrolled or was revoked since the credentials were saved, so the
 * region's agents cannot decrypt them and the check will fail (spec
 * 2026-07-16-02).
 *
 * A warning, not a destructive state: nothing is lost, but the operator must
 * act. The server cannot fix it itself — it cannot read sealed-only
 * credentials either — so the only remedy is re-saving them.
 *
 * Renders nothing unless `needsReseal` is true.
 */
export function NeedsResealAlert({ needsReseal }: NeedsResealAlertProps) {
  const { t } = useTranslation(["checks"]);

  if (!needsReseal) {
    return null;
  }

  return (
    <Alert variant="warning" data-testid="needs-reseal-warning">
      <AlertTriangle />
      <AlertTitle>{t("checks:detail.needsReseal.title")}</AlertTitle>
      <AlertDescription>
        {t("checks:detail.needsReseal.description")}
      </AlertDescription>
    </Alert>
  );
}
