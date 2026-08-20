import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import type {
  PublicIncident,
  PublicIncidentUpdate,
  StatusUpdatePublicResponse,
} from "@/api/hooks";
import { severityStyle } from "@/lib/status-style";
import { StatusUpdateThreadList } from "./status-updates-timeline";

function stateBadgeVariant(state: string) {
  switch (state) {
    case "resolved":
      return "success" as const;
    case "monitoring":
      return "default" as const;
    default:
      return "warning" as const;
  }
}

function formatTimestamp(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  });
}

/**
 * Adapts a publication update onto the shape the shared timeline renders.
 *
 * The two types are field-compatible by design; going through the shared
 * component is what keeps the hardened Markdown renderer (skipHtml + link
 * scheme allowlist, in StatusUpdateCard) in exactly one place.
 */
function toTimelineUpdate(
  update: PublicIncidentUpdate,
): StatusUpdatePublicResponse {
  return {
    uid: update.uid,
    title: update.title,
    bodyMarkdown: update.bodyMarkdown,
    kind: update.kind,
    publishedAt: update.publishedAt,
  };
}

export function IncidentCard({ incident }: { incident: PublicIncident }) {
  const { t } = useTranslation();
  const updates = incident.updates ?? [];
  const severity = severityStyle(incident.severity);

  return (
    <article
      id={`incident-${incident.uid}`}
      data-testid="active-incident"
      data-incident-state={incident.state}
      data-incident-severity={incident.severity ?? "none"}
      className={`scroll-mt-24 overflow-hidden rounded-xl border ${
        severity?.cardSurface ?? "border-border bg-muted/30"
      }`}
    >
      <div className="p-4">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <h3
            className="min-w-0 break-words text-sm font-semibold sm:text-base"
            data-testid="active-incident-title"
          >
            {incident.title}
          </h3>
          {/* translate="no" on the badges: their text flips as the incident
              progresses while their elements are reused, which is the pattern
              that makes an auto-translated page throw NotFoundError. See
              NO_TRANSLATE in status-page-view.tsx. */}
          <div className="flex shrink-0 items-center gap-2">
            {severity && (
              <Badge
                variant={severity.badgeVariant}
                data-testid="active-incident-severity"
                translate="no"
              >
                {t(severity.labelKey, { defaultValue: incident.severity })}
              </Badge>
            )}
            <Badge
              variant={stateBadgeVariant(incident.state)}
              data-testid="active-incident-state"
              translate="no"
            >
              {t(`incidentState.${incident.state}`, {
                defaultValue: incident.state,
              })}
            </Badge>
          </div>
        </div>

        <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
          <span translate="no">
            {t("incidentStartedAt", {
              when: formatTimestamp(incident.startedAt),
            })}
          </span>
          {incident.resolvedAt && (
            <span translate="no">
              {t("incidentResolvedAt", {
                when: formatTimestamp(incident.resolvedAt),
              })}
            </span>
          )}
        </div>

        {incident.affectedResources &&
          incident.affectedResources.length > 0 && (
            <p
              className="mt-2 break-words text-xs text-muted-foreground"
              data-testid="active-incident-affected"
            >
              {t("incidentAffects", {
                names: incident.affectedResources.join(", "),
              })}
            </p>
          )}
      </div>

      {updates.length > 0 && (
        <div className="border-t border-border/60 bg-card/60">
          <StatusUpdateThreadList updates={updates.map(toTimelineUpdate)} />
        </div>
      )}
    </article>
  );
}

/**
 * The active-incidents block: one card per open publication.
 *
 * Renders nothing at all when there are none — an empty "No incidents" box on
 * a healthy page is noise, and the rollup banner above already says everything
 * is fine.
 */
export function ActiveIncidents({
  incidents,
}: {
  incidents: PublicIncident[] | undefined;
}) {
  const { t } = useTranslation();

  if (!incidents || incidents.length === 0) return null;

  return (
    <section
      aria-label={t("activeIncidents")}
      className="mt-6 space-y-3"
      data-testid="active-incidents"
    >
      <h2 className="text-lg font-semibold">
        {t("activeIncidents")}{" "}
        <span
          className="text-sm font-normal text-muted-foreground"
          translate="no"
        >
          ({incidents.length})
        </span>
      </h2>
      <div className="space-y-3">
        {incidents.map((incident) => (
          <IncidentCard key={incident.uid} incident={incident} />
        ))}
      </div>
    </section>
  );
}
