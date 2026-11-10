import { useTranslation } from "react-i18next";
import { AlertTriangle } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { RewrapButton } from "@/components/shared/rewrap-button";

interface NeedsResealAlertProps {
  /**
   * The check's `needsReseal` flag (detail responses only). Undefined for
   * checks that target no private location.
   */
  needsReseal?: boolean;
  /** With org and checkUid, an admin can re-seal it on a peer agent. */
  org?: string;
  checkUid?: string;
  canRewrap?: boolean;
}

/**
 * NeedsResealAlert warns that a check running in a private location has
 * credentials sealed to a different agent set than the one currently active —
 * an agent enrolled or was revoked since the credentials were saved, so the
 * region's agents cannot decrypt them and the check will fail (spec
 * 2026-07-16-02).
 *
 * A warning, not a destructive state: nothing is lost, but the operator must
 * act. The server cannot read sealed credentials, but an online agent that
 * still holds a matching key can re-seal them for the current agents (spec
 * 2026-11-10-03, "Re-seal for the current agents"); otherwise the remedy is
 * re-saving them.
 *
 * Renders nothing unless `needsReseal` is true.
 */
export function NeedsResealAlert({ needsReseal, org, checkUid, canRewrap }: NeedsResealAlertProps) {
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
        {canRewrap && org && checkUid && (
          <div className="mt-2">
            <RewrapButton org={org} checkUids={[checkUid]} testId="check" />
          </div>
        )}
      </AlertDescription>
    </Alert>
  );
}
