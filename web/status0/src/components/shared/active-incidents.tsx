import { useTranslation } from "react-i18next";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { Badge } from "@/components/ui/badge";
import type { BadgeProps } from "@/components/ui/badge";
import type { PublicIncident, PublicIncidentUpdate } from "@/api/hooks";

/**
 * Severity → banner surface. Deliberately reuses the same three-step visual
 * language as the page rollup banner (amber / orange / red) rather than
 * inventing a fourth palette: a visitor should be able to read "how bad is
 * this" from colour alone, and two different colour systems on one page defeat
 * that.
 *
 * A publication with no severity gets the neutral treatment — the operator
 * chose not to grade it, and guessing one would be putting words in their
 * mouth.
 */
function severitySurface(severity: string | undefined): string {
  switch (severity) {
    case "critical":
      return "border-red-500/40 bg-red-500/10 text-red-900 dark:text-red-100";
    case "major":
      return "border-orange-500/40 bg-orange-500/10 text-orange-900 dark:text-orange-100";
    case "minor":
      return "border-amber-500/40 bg-amber-500/10 text-amber-900 dark:text-amber-100";
    default:
      return "border-border bg-muted/40 text-foreground";
  }
}

function severityBadgeVariant(
  severity: string | undefined,
): BadgeProps["variant"] {
  switch (severity) {
    case "critical":
    case "major":
      return "destructive";
    case "minor":
      return "warning";
    default:
      return "secondary";
  }
}

function stateBadgeVariant(state: string): BadgeProps["variant"] {
  switch (state) {
    case "resolved":
      return "success";
    case "monitoring":
      return "default";
    default:
      return "warning";
  }
}

const ALLOWED_SCHEMES = ["http", "https", "mailto"];

/**
 * Same hardened Markdown renderer the update card uses: `skipHtml`, a scheme
 * allowlist on links, and headings capped so incident copy cannot restructure
 * the page outline.
 */
function SafeMarkdown({ content }: { content: string }) {
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      skipHtml
      components={{
        a: ({ href, children, ...props }) => {
          const scheme = href?.split(":")[0] ?? "";
          if (!ALLOWED_SCHEMES.includes(scheme)) return <>{children}</>;
          return (
            <a href={href} rel="noopener noreferrer" target="_blank" {...props}>
              {children}
            </a>
          );
        },
        h1: "h4",
        h2: "h4",
        h3: "h4",
      }}
    >
      {content}
    </ReactMarkdown>
  );
}

function formatTimestamp(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  });
}

function IncidentUpdateRow({ update }: { update: PublicIncidentUpdate }) {
  const { t } = useTranslation();

  return (
    <li className="relative pl-5 py-2">
      <span
        aria-hidden
        className="absolute left-0 top-3.5 h-1.5 w-1.5 rounded-full bg-muted-foreground"
      />
      <div className="flex flex-wrap items-baseline gap-2">
        {/* translate="no": the kind flips as the incident progresses and the
            timestamp is recomputed, so React rewrites these text nodes while
            reusing their elements — the exact pattern that makes an
            auto-translated page throw NotFoundError. See NO_TRANSLATE in
            status-page-view.tsx. */}
        <Badge
          variant={stateBadgeVariant(update.kind)}
          data-testid="incident-update-kind"
          translate="no"
        >
          {t(`incidentState.${update.kind}`, {
            defaultValue: update.kind,
          })}
        </Badge>
        <time
          dateTime={update.publishedAt}
          className="text-xs text-muted-foreground"
          translate="no"
        >
          {formatTimestamp(update.publishedAt)}
        </time>
      </div>
      {update.bodyMarkdown && (
        <div className="prose prose-sm max-w-none mt-1 text-sm [&>*:last-child]:mb-0">
          <SafeMarkdown content={update.bodyMarkdown} />
        </div>
      )}
    </li>
  );
}

export function IncidentCard({ incident }: { incident: PublicIncident }) {
  const { t } = useTranslation();
  const updates = incident.updates ?? [];

  return (
    <article
      id={`incident-${incident.uid}`}
      data-testid="active-incident"
      data-incident-state={incident.state}
      data-incident-severity={incident.severity ?? "none"}
      className={`scroll-mt-24 rounded-xl border p-4 ${severitySurface(incident.severity)}`}
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <h3
          className="text-sm sm:text-base font-semibold min-w-0 break-words"
          data-testid="active-incident-title"
        >
          {incident.title}
        </h3>
        <div className="flex items-center gap-2 shrink-0">
          {incident.severity && (
            <Badge
              variant={severityBadgeVariant(incident.severity)}
              data-testid="active-incident-severity"
              translate="no"
            >
              {t(`incidentSeverity.${incident.severity}`, {
                defaultValue: incident.severity,
              })}
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

      {incident.affectedResources && incident.affectedResources.length > 0 && (
        <p
          className="mt-2 text-xs text-muted-foreground break-words"
          data-testid="active-incident-affected"
        >
          {t("incidentAffects", {
            names: incident.affectedResources.join(", "),
          })}
        </p>
      )}

      {updates.length > 0 && (
        <ul className="mt-3 border-l border-border/70 ml-1 space-y-0">
          {updates.map((update) => (
            <IncidentUpdateRow key={update.uid} update={update} />
          ))}
        </ul>
      )}
    </article>
  );
}

/**
 * The active-incidents block: a short banner line plus one card per open
 * publication. Renders nothing at all when there are none — an empty
 * "No incidents" box on a healthy page is noise, and the rollup banner above
 * already says everything is fine.
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
