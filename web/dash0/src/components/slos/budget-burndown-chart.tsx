import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import {
  Area,
  CartesianGrid,
  ComposedChart,
  Line,
  ReferenceLine,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";

import type { SloBurndown } from "@/api/hooks";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { formatBudgetSeconds } from "@/lib/slo-format";

interface BurndownPoint {
  ts: number;
  remaining: number;
  ideal: number;
}

/**
 * Error-budget burn-down for the current window.
 *
 * Two series, and the comparison between them is the whole point: the actual
 * remaining budget against the straight "ideal" line that spends it exactly
 * over the window. Above the line is healthy, below it is the burn rate the
 * status chip is already reporting, made legible over time.
 *
 * The remaining series is deliberately NOT clamped at zero — an overspent
 * budget dipping below the axis is exactly what a breach looks like, and
 * flattening it there would hide the magnitude.
 */
export function BudgetBurndownChart({
  burndown,
  isLoading,
}: {
  burndown?: SloBurndown;
  isLoading?: boolean;
}) {
  const { t, i18n } = useTranslation("slos");

  const points = useMemo<BurndownPoint[]>(
    () =>
      (burndown?.data ?? []).map((point) => ({
        ts: new Date(point.at).getTime(),
        remaining: point.budgetRemainingSeconds,
        ideal: point.idealRemainingSeconds,
      })),
    [burndown],
  );

  const dayFormatter = useMemo(
    () => new Intl.DateTimeFormat(i18n.language, { month: "short", day: "numeric" }),
    [i18n.language],
  );

  if (isLoading) {
    return <Skeleton className="h-64 w-full" />;
  }

  return (
    <Card data-testid="slo-burndown-card">
      <CardHeader>
        <CardTitle className="text-base">{t("detail.burndown")}</CardTitle>
        <p className="text-sm text-muted-foreground">{t("detail.burndownHelp")}</p>
      </CardHeader>
      <CardContent>
        {points.length === 0 ? (
          <p className="text-sm text-muted-foreground" data-testid="slo-burndown-empty">
            {t("detail.burndownEmpty")}
          </p>
        ) : (
          // Fixed height + ResponsiveContainer width keeps the chart usable on
          // a phone without a horizontal scroll.
          <ResponsiveContainer width="100%" height={260}>
            <ComposedChart data={points} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
              <defs>
                <linearGradient id="slo-burndown-fill" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor="var(--primary)" stopOpacity={0.28} />
                  <stop offset="100%" stopColor="var(--primary)" stopOpacity={0.02} />
                </linearGradient>
              </defs>
              <CartesianGrid strokeDasharray="3 3" className="stroke-muted" />
              <XAxis
                dataKey="ts"
                type="number"
                scale="time"
                domain={["dataMin", "dataMax"]}
                tickFormatter={(value: number) => dayFormatter.format(new Date(value))}
                minTickGap={40}
                className="text-xs"
                tick={{ fill: "var(--muted-foreground)" }}
              />
              <YAxis
                tickFormatter={(value: number) => formatBudgetSeconds(value)}
                className="text-xs"
                tick={{ fill: "var(--muted-foreground)" }}
                width={64}
              />
              {/* Zero is the breach line: crossing it is the event, so it is
                  drawn explicitly rather than left to the grid. */}
              <ReferenceLine y={0} stroke="var(--destructive)" strokeDasharray="4 4" />
              <Tooltip
                content={({ active, payload }) => {
                  if (!active || !payload?.length) return null;
                  const point = payload[0].payload as BurndownPoint;
                  return (
                    <div className="rounded-md border bg-popover px-3 py-2 text-xs shadow-md">
                      <div className="font-medium">
                        {dayFormatter.format(new Date(point.ts))}
                      </div>
                      <div className="mt-1 text-muted-foreground">
                        {t("detail.budgetRemaining")}:{" "}
                        <span className="font-medium text-foreground">
                          {formatBudgetSeconds(point.remaining)}
                        </span>
                      </div>
                      <div className="text-muted-foreground">
                        {t("detail.burndownIdeal")}:{" "}
                        {formatBudgetSeconds(point.ideal)}
                      </div>
                    </div>
                  );
                }}
              />
              <Area
                type="monotone"
                dataKey="remaining"
                name={t("detail.budgetRemaining")}
                stroke="var(--primary)"
                strokeWidth={2}
                fill="url(#slo-burndown-fill)"
                // Custom dot so every DATA point carries a <title>. recharts'
                // hover activeDot does not, which is what makes
                // `circle:has(title)` a deterministic count for an E2E while a
                // bare `circle` count is not.
                dot={(props: { cx?: number; cy?: number; index?: number }) => (
                  <circle
                    key={props.index}
                    cx={props.cx}
                    cy={props.cy}
                    r={2.5}
                    fill="var(--primary)"
                  >
                    <title>{t("detail.budgetRemaining")}</title>
                  </circle>
                )}
                activeDot={{ r: 4 }}
                isAnimationActive={false}
              />
              <Line
                type="linear"
                dataKey="ideal"
                name={t("detail.burndownIdeal")}
                stroke="var(--muted-foreground)"
                strokeDasharray="5 4"
                strokeWidth={1.5}
                dot={false}
                isAnimationActive={false}
              />
            </ComposedChart>
          </ResponsiveContainer>
        )}
      </CardContent>
    </Card>
  );
}
