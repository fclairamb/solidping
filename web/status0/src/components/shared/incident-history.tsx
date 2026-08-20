import { useState } from "react";
import { useTranslation } from "react-i18next";
import { usePublicIncidentHistory } from "@/api/hooks";
import { IncidentCard } from "./active-incidents";

/**
 * Past incidents for a status page.
 *
 * Collapsed by default and fetched only once opened: the overwhelming majority
 * of visitors are here to find out whether something is broken RIGHT NOW, and
 * a wall of resolved incidents above the fold answers a question nobody asked
 * — while still costing everyone the request.
 */
export function IncidentHistory({ org, slug }: { org: string; slug: string }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const { data, isLoading } = usePublicIncidentHistory(open ? org : "", slug);

  const resolved = (data?.data ?? []).filter(
    (incident) => incident.state === "resolved",
  );

  return (
    <section
      aria-label={t("incidentHistory")}
      className="mt-8"
      data-testid="incident-history"
    >
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        aria-expanded={open}
        className="text-sm font-medium text-primary hover:underline"
        data-testid="incident-history-toggle"
      >
        {open ? t("hideIncidentHistory") : t("showIncidentHistory")}
      </button>

      {open && (
        <div className="mt-4 space-y-3">
          {isLoading && (
            <p className="text-sm text-muted-foreground">{t("loading")}</p>
          )}
          {!isLoading && resolved.length === 0 && (
            <p className="text-sm text-muted-foreground">
              {t("noPastIncidents")}
            </p>
          )}
          {resolved.map((incident) => (
            <IncidentCard key={incident.uid} incident={incident} />
          ))}
        </div>
      )}
    </section>
  );
}
