import { Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Target } from "lucide-react";

import { useSlos } from "@/api/hooks";
import { Badge } from "@/components/ui/badge";

/**
 * "This check is covered by an objective" chip for the check detail header.
 *
 * Only DIRECT coverage is shown (an SLO whose scope is this exact check), not
 * coverage via a group: a group SLO's members change without the SLO changing,
 * so claiming a specific check is covered by one would be a promise the data
 * model does not make.
 */
export function SloCoverageChip({ org, checkUid }: { org: string; checkUid: string }) {
  const { t } = useTranslation("slos");
  const { data: slos } = useSlos(org, { checkUid });

  if (!slos || slos.length === 0) {
    return null;
  }

  const target = slos[0];

  return (
    <Link
      to={slos.length === 1 ? "/orgs/$org/slos/$uid" : "/orgs/$org/slos"}
      params={slos.length === 1 ? { org, uid: target.uid } : { org }}
      className="shrink-0"
      data-testid="check-slo-chip"
    >
      <Badge variant="outline" className="gap-1">
        <Target className="h-3 w-3" />
        {t("chip.covered", { count: slos.length })}
      </Badge>
    </Link>
  );
}
