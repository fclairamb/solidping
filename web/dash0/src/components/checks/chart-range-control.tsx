import { useTranslation } from "react-i18next";

import type { TimeRange } from "@/lib/chart-window";
import { SegmentedControl } from "@/components/ui/segmented-control";

const RANGES: TimeRange[] = ["hour", "day", "week", "month"];

interface ChartRangeControlProps {
  value: TimeRange;
  onChange: (range: TimeRange) => void;
  /** Each segment gets `data-testid="<testIdPrefix>-<range>"`. */
  testIdPrefix: string;
}

/** The Hour / Day / Week / Month picker of the check charts. */
export function ChartRangeControl({
  value,
  onChange,
  testIdPrefix,
}: ChartRangeControlProps) {
  const { t } = useTranslation("checks");
  return (
    <SegmentedControl<TimeRange>
      value={value}
      onValueChange={onChange}
      aria-label={t("detail.chart.rangeLabel")}
      options={RANGES.map((range) => {
        const key = `detail.chart.range${range[0].toUpperCase()}${range.slice(1)}`;
        return {
          value: range,
          testId: `${testIdPrefix}-${range}`,
          label: (
            <>
              <span className="sm:hidden">{t(`${key}Short`)}</span>
              <span className="hidden sm:inline">{t(key)}</span>
            </>
          ),
        };
      })}
    />
  );
}
