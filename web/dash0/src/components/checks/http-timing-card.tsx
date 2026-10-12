import { useTranslation } from "react-i18next";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { extractHttpTimingPhases } from "@/lib/http-timing";

interface HttpTimingCardProps {
  metrics: Record<string, unknown> | null | undefined;
  /** Render only the rows, without the Card chrome (for embedding). */
  bare?: boolean;
}

function formatPhaseMs(ms: number): string {
  return `${Math.round(ms * 100) / 100} ms`;
}

/**
 * The "Timing" card of an HTTP result: one horizontal bar per phase that ran,
 * in order (redirect, dns, connect, tls, ttfb, download). Phases the probe did
 * not run are not drawn. Renders nothing when no phase is present.
 */
export function HttpTimingCard({ metrics, bare = false }: HttpTimingCardProps) {
  const { t } = useTranslation("checks");
  const phases = extractHttpTimingPhases(metrics);
  if (phases.length === 0) return null;

  const max = Math.max(...phases.map((p) => p.ms), Number.EPSILON);

  const rows = (
    <div className="space-y-2 text-sm">
      {phases.map((phase) => (
        <div
          key={phase.id}
          className="grid grid-cols-[minmax(0,9rem)_1fr_auto] items-center gap-3"
          data-testid={`http-timing-phase-${phase.id}`}
        >
          <span className="truncate text-muted-foreground">
            {t(`timing.phase.${phase.id}`)}
          </span>
          <div className="h-2 rounded bg-muted">
            <div
              className="h-2 rounded bg-primary"
              style={{ width: `${Math.max((phase.ms / max) * 100, 1)}%` }}
            />
          </div>
          <code className="font-mono">{formatPhaseMs(phase.ms)}</code>
        </div>
      ))}
    </div>
  );

  if (bare) {
    return <div data-testid="http-timing-card">{rows}</div>;
  }

  return (
    <Card data-testid="http-timing-card">
      <CardHeader>
        <CardTitle className="text-base">{t("timing.title")}</CardTitle>
      </CardHeader>
      <CardContent>{rows}</CardContent>
    </Card>
  );
}
