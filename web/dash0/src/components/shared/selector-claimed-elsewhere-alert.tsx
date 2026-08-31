import { useTranslation } from "react-i18next";
import { AlertTriangle } from "lucide-react";

import { Alert, AlertDescription } from "@/components/ui/alert";

interface SelectorClaimedElsewhereAlertProps {
  /**
   * How many resources the section itself currently shows (i.e.
   * `section.resources?.length ?? 0`). Zero means every one of the
   * selector's matches was claimed elsewhere, which is the case that needs
   * the fuller explanation.
   */
  ownResourceCount: number;
  /**
   * `selectorClaimedElsewhere` from the section response: how many of the
   * selector's matched checks are displayed by resource rows OUTSIDE this
   * section — an earlier selector section or a manual placement, the
   * distinction doesn't matter to the reader (spec 2026-08-31-01).
   */
  claimedElsewhere?: number;
  /**
   * `selectorClaimedSectionName` — the section holding the most of the
   * claimed-elsewhere checks. Only used (and only needed) in the
   * fully-claimed copy.
   */
  claimantName?: string;
}

/**
 * SelectorClaimedElsewhereAlert explains why a dynamic section shows fewer
 * components than its selector matches — because the missing ones are
 * ALREADY claimed by an earlier section or a manual placement, not because
 * the label rule is broken (spec 2026-08-31-01).
 *
 * Without this, an operator sees an empty (or partially empty) selector
 * section with no way to tell "the rule is wrong" apart from "another
 * section already claimed these" — a real report came in read as the
 * former when it was the latter.
 *
 * Renders nothing when nothing is claimed elsewhere.
 */
export function SelectorClaimedElsewhereAlert({
  ownResourceCount,
  claimedElsewhere,
  claimantName,
}: SelectorClaimedElsewhereAlertProps) {
  const { t } = useTranslation("statusPages");

  if (!claimedElsewhere) {
    return null;
  }

  const fullyClaimed = ownResourceCount === 0;

  return (
    <Alert variant="warning" data-testid="section-selector-claimed-elsewhere">
      <AlertTriangle />
      <AlertDescription>
        {fullyClaimed
          ? t("sections.membership.claimedElsewhere.full", {
              count: claimedElsewhere,
              section: claimantName ?? "",
            })
          : t("sections.membership.claimedElsewhere.partial", {
              count: claimedElsewhere,
            })}
      </AlertDescription>
    </Alert>
  );
}
