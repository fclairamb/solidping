// The two check-detail surfaces of the SSH-tunnel edge, one per direction.
//
// `TunnelVia` shows the dependent's side ("this check dials through <bastion>").
// `TunnelDependents` shows the bastion's side ("used as a tunnel by N checks") —
// which is also what makes the delete 409 self-explanatory instead of a mystery:
// the user can see the dependents right where the Delete button is.
import { Link } from "@tanstack/react-router";
import { Waypoints } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import { useChecks, type Check } from "@/api/hooks";
import { checkLabel, tunnelCheckUidOf } from "./tunnel";

/** The bastion this check's probe is dialed through, if any. */
export function TunnelVia({ org, check }: { org: string; check: Check }) {
  const { t } = useTranslation("checks");
  const tunnelUid = tunnelCheckUidOf(check);
  // The reference is a uid; resolve it to a name via the org's ssh checks
  // (already cached by the check form's identical query).
  const { data: sshChecks } = useChecks(org, { type: "ssh", limit: 100 });

  if (!tunnelUid) return null;

  const bastion = (sshChecks ?? []).find((c) => c.uid === tunnelUid);

  return (
    <div data-testid="check-tunnel-via">
      <div className="text-sm font-medium text-muted-foreground mb-1">
        {t("tunnel.viaTitle")}
      </div>
      <Link
        to="/orgs/$org/checks/$checkUid"
        params={{ org, checkUid: bastion?.slug || tunnelUid }}
        className="inline-flex"
      >
        <Badge variant="outline" className="gap-1">
          <Waypoints className="h-3 w-3" />
          {bastion ? checkLabel(bastion) : tunnelUid}
        </Badge>
      </Link>
      <p className="mt-1 text-xs text-muted-foreground">
        {t("tunnel.viaHelp")}
      </p>
    </div>
  );
}

/** The checks that tunnel through this one. Renders nothing unless there are any. */
export function TunnelDependents({ org, check }: { org: string; check: Check }) {
  const { t } = useTranslation("checks");
  // Only SSH checks can be a tunnel, so nothing else can have dependents.
  const isSSH = check.type === "ssh";
  const { data: allChecks } = useChecks(org, { limit: 100 });

  if (!isSSH) return null;

  const dependents = (allChecks ?? []).filter(
    (candidate) => tunnelCheckUidOf(candidate) === check.uid,
  );

  if (dependents.length === 0) return null;

  return (
    <div data-testid="check-tunnel-dependents">
      <div className="text-sm font-medium text-muted-foreground mb-1">
        {t("tunnel.dependentsTitle", { count: dependents.length })}
      </div>
      <div className="flex gap-1 flex-wrap">
        {dependents.map((dependent) => (
          <Link
            key={dependent.uid}
            to="/orgs/$org/checks/$checkUid"
            params={{ org, checkUid: dependent.slug || dependent.uid }}
            className="inline-flex"
          >
            <Badge variant="outline" className="gap-1">
              <Waypoints className="h-3 w-3" />
              {checkLabel(dependent)}
            </Badge>
          </Link>
        ))}
      </div>
      <p className="mt-1 text-xs text-muted-foreground">
        {t("tunnel.dependentsHelp")}
      </p>
    </div>
  );
}
