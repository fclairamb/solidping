import { useTranslation } from "react-i18next";
import { Badge } from "@/components/ui/badge";
import type { PublicMaintenance } from "@/api/hooks";
import { maintenanceHint } from "@/lib/upcoming-maintenance";

function formatRange(startsAt: string, endsAt: string): string {
  const start = new Date(startsAt);
  const end = new Date(endsAt);
  if (Number.isNaN(start.getTime()) || Number.isNaN(end.getTime())) {
    return `${startsAt} - ${endsAt}`;
  }
  const opts: Intl.DateTimeFormatOptions = {
    dateStyle: "medium",
    timeStyle: "short",
  };
  return `${start.toLocaleString(undefined, opts)} - ${end.toLocaleString(undefined, opts)}`;
}

/**
 * Announces scheduled maintenance (in progress or within the next 7 days) on
 * the resources this page shows. Renders nothing when there is none. The
 * description is operator-authored plain text: rendered as text, never as
 * markdown or HTML.
 */
export function UpcomingMaintenance({
  items,
}: {
  items: PublicMaintenance[] | undefined;
}) {
  const { t } = useTranslation();

  if (!items || items.length === 0) return null;

  return (
    <section
      aria-label={t("upcomingMaintenance")}
      className="mt-6 mb-6 space-y-3"
      data-testid="upcoming-maintenance"
    >
      <h2 className="text-lg font-semibold">{t("upcomingMaintenance")}</h2>
      <div className="space-y-3">
        {items.map((item) => {
          const hint = maintenanceHint(item.active, item.startsAt);
          const resources = item.affectedResources ?? [];
          return (
            <article
              key={`${item.uid}-${item.startsAt}`}
              className="rounded-lg border p-4"
              data-testid="upcoming-maintenance-item"
            >
              <div className="flex flex-wrap items-center gap-2">
                <h3 className="font-medium">{item.title}</h3>
                {item.active && (
                  <Badge
                    variant="warning"
                    data-testid="upcoming-maintenance-active-badge"
                  >
                    {t("maintenanceInProgress")}
                  </Badge>
                )}
              </div>
              <p className="text-sm text-muted-foreground">
                {formatRange(item.startsAt, item.endsAt)}
                {!item.active && <> ({t(hint.key, { count: hint.count })})</>}
              </p>
              {item.description && (
                <p className="mt-2 whitespace-pre-line text-sm">
                  {item.description}
                </p>
              )}
              {resources.length > 0 && (
                <p className="mt-2 text-sm text-muted-foreground">
                  {t("maintenanceAffects", { resources: resources.join(", ") })}
                </p>
              )}
            </article>
          );
        })}
      </div>
    </section>
  );
}
