// The two check-detail surfaces of the send-mode SMTP <-> email check pairing
// (spec 2026-08-19-04), mirroring tunnel-detail.tsx's TunnelVia/TunnelDependents.
//
// `DeliveryVia` shows the SMTP check's side ("this check delivers to <email
// check>"). `DeliverySources` shows the email check's side ("N SMTP checks
// deliver here") — several SMTP checks MAY target the same email check
// (headers keep arrivals attributable), but 1:1 pairing is the recommended,
// documented setup.
//
// Revised design (2026-08-19): both cards key off delivery_check_uid, which
// is now OPTIONAL bonus metadata (the SMTP check's actual recipient is
// delivery_to, a plain address) — these cards render nothing when it wasn't
// supplied, exactly like they did for a check with no pairing at all.
import { Link } from "@tanstack/react-router";
import { Send } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { useChecks, type Check } from "@/api/hooks";
import { checkLabel } from "@/components/checks/tunnel";

function deliveryCheckUidOf(check: Check): string | undefined {
  const value = check.config?.["delivery_check_uid"];
  return typeof value === "string" && value !== "" ? value : undefined;
}

/** The paired email check this SMTP check delivers its probe email to, if any. */
export function DeliveryVia({ org, check }: { org: string; check: Check }) {
  const deliveryUid = check.type === "smtp" ? deliveryCheckUidOf(check) : undefined;
  const { data: emailChecks } = useChecks(org, { type: "email", limit: 100 });

  if (!deliveryUid) return null;

  const target = (emailChecks ?? []).find((c) => c.uid === deliveryUid);

  return (
    <div data-testid="check-smtp-delivery-via">
      <div className="text-sm font-medium text-muted-foreground mb-1">Delivery check</div>
      <Link
        to="/orgs/$org/checks/$checkUid"
        params={{ org, checkUid: target?.slug || deliveryUid }}
        className="inline-flex"
      >
        <Badge variant="outline" className="gap-1">
          <Send className="h-3 w-3" />
          {target ? checkLabel(target) : deliveryUid}
        </Badge>
      </Link>
      <p className="mt-1 text-xs text-muted-foreground">
        Probe emails are addressed to this email check&apos;s tokenized inbox.
      </p>
    </div>
  );
}

/** The SMTP checks that deliver to this email check. Renders nothing unless there are any. */
export function DeliverySources({ org, check }: { org: string; check: Check }) {
  // Only email checks can be a delivery target, so nothing else has sources.
  const isEmail = check.type === "email";
  const { data: allChecks } = useChecks(org, { type: "smtp", limit: 100 });

  if (!isEmail) return null;

  const sources = (allChecks ?? []).filter((candidate) => deliveryCheckUidOf(candidate) === check.uid);

  if (sources.length === 0) return null;

  return (
    <div data-testid="check-smtp-delivery-sources">
      <div className="text-sm font-medium text-muted-foreground mb-1">
        Delivered to by {sources.length} SMTP check{sources.length === 1 ? "" : "s"}
      </div>
      <div className="flex flex-wrap gap-1">
        {sources.map((source) => (
          <Link
            key={source.uid}
            to="/orgs/$org/checks/$checkUid"
            params={{ org, checkUid: source.slug || source.uid }}
            className="inline-flex"
          >
            <Badge variant="outline" className="gap-1">
              <Send className="h-3 w-3" />
              {checkLabel(source)}
            </Badge>
          </Link>
        ))}
      </div>
    </div>
  );
}
