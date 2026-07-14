import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { AlertCircle, Check, Info, Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";
import { ApiError } from "@/api/client";
import { useSystemParameters, useSetSystemParameter } from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/server/aggregation")({
  component: AggregationSettingsPage,
});

// The three live system parameters the aggregation job reads on each run
// (env → global DB parameter → default). Writing them here takes effect on the
// job's next scheduled run without a server restart. The unit is baked into the
// key name and surfaced in each field's label.
const RAW_HOURS_KEY = "performance.aggregation_retention_raw_hours";
const HOUR_DAYS_KEY = "performance.aggregation_retention_hour_days";
const DAY_MONTHS_KEY = "performance.aggregation_retention_day_months";

// Defaults mirror the backend's jobtypes default constants (24 / 7 / 2) so an
// unconfigured deployment shows the same values the job would apply.
const DEFAULT_RAW_HOURS = "24";
const DEFAULT_HOUR_DAYS = "7";
const DEFAULT_DAY_MONTHS = "2";

function AggregationSettingsPage() {
  const { t } = useTranslation(["server", "common"]);
  const { data: params, isLoading } = useSystemParameters();
  const setParam = useSetSystemParameter();

  const [rawHours, setRawHours] = useState(DEFAULT_RAW_HOURS);
  const [hourDays, setHourDays] = useState(DEFAULT_HOUR_DAYS);
  const [dayMonths, setDayMonths] = useState(DEFAULT_DAY_MONTHS);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  // Seed the fields from the loaded parameters during render, guarded by a
  // reference check, rather than in an effect (which trips the cascading-render
  // lint). React Query's structural sharing keeps the array reference stable
  // across no-op refetches, so this only re-syncs when the data actually
  // changes — e.g. after a save invalidates and refetches the query.
  const [syncedParams, setSyncedParams] = useState<typeof params>(undefined);
  if (params && params !== syncedParams) {
    setSyncedParams(params);
    const get = (key: string) => params.find((p) => p.key === key)?.value;
    const raw = get(RAW_HOURS_KEY);
    const hour = get(HOUR_DAYS_KEY);
    const day = get(DAY_MONTHS_KEY);
    if (raw != null) setRawHours(String(raw));
    if (hour != null) setHourDays(String(hour));
    if (day != null) setDayMonths(String(day));
  }

  // Each field is an integer window with a floor of 1 in its own unit; mirror
  // the server-side validation so a value the backend would reject (422) is
  // caught before the save round-trips.
  const isValidWindow = (raw: string) => {
    const n = Number(raw);
    return Number.isInteger(n) && n >= 1;
  };
  const rawInvalid = !isValidWindow(rawHours);
  const hourInvalid = !isValidWindow(hourDays);
  const dayInvalid = !isValidWindow(dayMonths);
  const anyInvalid = rawInvalid || hourInvalid || dayInvalid;

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    if (anyInvalid) return;
    setError(null);
    setSaved(false);

    try {
      await Promise.all([
        setParam.mutateAsync({
          key: RAW_HOURS_KEY,
          value: parseInt(rawHours, 10),
        }),
        setParam.mutateAsync({
          key: HOUR_DAYS_KEY,
          value: parseInt(hourDays, 10),
        }),
        setParam.mutateAsync({
          key: DAY_MONTHS_KEY,
          value: parseInt(dayMonths, 10),
        }),
      ]);
      setSaved(true);
      setTimeout(() => setSaved(false), 3000);
    } catch (err) {
      if (err instanceof ApiError) {
        setError(err.message);
      } else {
        setError(t("server:unexpectedError"));
      }
    }
  };

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-12">
        <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
      </div>
    );
  }

  const fields: {
    id: string;
    testid: string;
    label: string;
    help: string;
    value: string;
    setValue: (v: string) => void;
    invalid: boolean;
  }[] = [
    {
      id: "rawHours",
      testid: "aggregation-raw-hours-input",
      label: t("server:aggregation.rawHours"),
      help: t("server:aggregation.rawHoursHelp"),
      value: rawHours,
      setValue: setRawHours,
      invalid: rawInvalid,
    },
    {
      id: "hourDays",
      testid: "aggregation-hour-days-input",
      label: t("server:aggregation.hourDays"),
      help: t("server:aggregation.hourDaysHelp"),
      value: hourDays,
      setValue: setHourDays,
      invalid: hourInvalid,
    },
    {
      id: "dayMonths",
      testid: "aggregation-day-months-input",
      label: t("server:aggregation.dayMonths"),
      help: t("server:aggregation.dayMonthsHelp"),
      value: dayMonths,
      setValue: setDayMonths,
      invalid: dayInvalid,
    },
  ];

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle>{t("server:aggregation.title")}</CardTitle>
          <CardDescription>
            {t("server:aggregation.description")}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSave} className="space-y-6">
            {error && (
              <Alert variant="destructive" data-testid="aggregation-error">
                <AlertCircle className="h-4 w-4" />
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}
            {saved && (
              <Alert data-testid="aggregation-saved">
                <Check className="h-4 w-4" />
                <AlertDescription>{t("server:saved")}</AlertDescription>
              </Alert>
            )}

            <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
              {fields.map((f) => (
                <div key={f.id} className="space-y-2">
                  <Label htmlFor={f.id}>{f.label}</Label>
                  <Input
                    id={f.id}
                    data-testid={f.testid}
                    type="number"
                    min="1"
                    step="1"
                    value={f.value}
                    onChange={(e) => f.setValue(e.target.value)}
                    disabled={setParam.isPending}
                    aria-invalid={f.invalid}
                    className={cn(f.invalid && "border-destructive")}
                  />
                  {f.invalid ? (
                    <p className="text-xs text-destructive">
                      {t("server:aggregation.floorError")}
                    </p>
                  ) : (
                    <p className="text-xs text-muted-foreground">{f.help}</p>
                  )}
                </div>
              ))}
            </div>

            {/* The two behaviours from the spec that surprise operators: edits
                never re-process existing rows, and raising a window can't bring
                back data that was already rolled up. */}
            <Alert>
              <Info className="h-4 w-4" />
              <AlertDescription>
                <ul className="list-disc space-y-1 pl-4">
                  <li>{t("server:aggregation.noteNoReaggregate")}</li>
                  <li>{t("server:aggregation.noteRaiseNoRestore")}</li>
                </ul>
              </AlertDescription>
            </Alert>

            <Button
              type="submit"
              data-testid="aggregation-save"
              disabled={setParam.isPending || anyInvalid}
            >
              {setParam.isPending ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  {t("common:saving")}
                </>
              ) : (
                t("common:save")
              )}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
