import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useParams } from "@tanstack/react-router";
import { AlertTriangle, ArrowLeft, Loader2 } from "lucide-react";

import type {
  CreateMaintenanceWindowRequest,
  MaintenanceWindow,
} from "@/api/hooks";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { CheckMultiPicker } from "@/components/shared/check-multi-picker";
import { MaintenanceScheduleSummary } from "@/components/shared/maintenance-schedule-summary";

export interface MaintenanceWindowFormChecks {
  checkUids: string[];
  checkGroupUids: string[];
}

export interface MaintenanceWindowFormSubmit {
  window: CreateMaintenanceWindowRequest;
  checkUids: string[];
  checkGroupUids: string[];
}

// Converts an RFC3339/UTC instant to a value suitable for an
// <input type="datetime-local"> (local wall-clock, no zone, minute precision).
function isoToLocalInput(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}` +
    `T${pad(d.getHours())}:${pad(d.getMinutes())}`
  );
}

// Converts a datetime-local value (interpreted in the browser's local zone)
// back to an RFC3339 UTC instant for the API.
function localInputToIso(local: string): string {
  if (!local) return "";
  const d = new Date(local);
  if (Number.isNaN(d.getTime())) return "";
  return d.toISOString();
}

const pad2 = (n: number) => String(n).padStart(2, "0");

// Local "HH:mm" of an instant, for <input type="time">.
function isoToLocalTime(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return `${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

// Local "YYYY-MM-DD" of an instant, for <input type="date">.
function isoToLocalDate(iso?: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`;
}

type Recurrence = "none" | "daily" | "weekly" | "monthly";
type DurationUnit = "minutes" | "hours";

// Parse "YYYY-MM-DD" + "HH:mm" into a local Date (browser zone).
function composeLocalDateTime(date: string, time: string): Date | null {
  if (!date || !time) return null;
  const d = new Date(`${date}T${time}`);
  return Number.isNaN(d.getTime()) ? null : d;
}

// Snap an anchor date so its cadence matches the picker, choosing the first
// matching day on/after the given local datetime. Returns a new Date.
//  - daily: as-is.
//  - weekly: advance to the next `weekday` (0=Sun..6=Sat) on/after the date.
//  - monthly: set day-of-month to `dayOfMonth` (clamped to month length) on/after
//    the date; if that day already passed this month, roll to next month.
function snapAnchor(
  dt: Date,
  recurrence: Recurrence,
  weekday: number,
  dayOfMonth: number,
): Date {
  if (recurrence === "daily") return new Date(dt);

  if (recurrence === "weekly") {
    const out = new Date(dt);
    const diff = (weekday - out.getDay() + 7) % 7;
    out.setDate(out.getDate() + diff);
    return out;
  }

  // monthly
  const clampDay = (year: number, monthIdx: number, day: number) => {
    const last = new Date(year, monthIdx + 1, 0).getDate();
    return Math.min(day, last);
  };
  let year = dt.getFullYear();
  let monthIdx = dt.getMonth();
  let day = clampDay(year, monthIdx, dayOfMonth);
  let candidate = new Date(year, monthIdx, day, dt.getHours(), dt.getMinutes());
  if (candidate.getTime() < dt.getTime()) {
    // Chosen day already passed this month → next month.
    monthIdx += 1;
    if (monthIdx > 11) {
      monthIdx = 0;
      year += 1;
    }
    day = clampDay(year, monthIdx, dayOfMonth);
    candidate = new Date(year, monthIdx, day, dt.getHours(), dt.getMinutes());
  }
  return candidate;
}

// Decompose a stored duration in ms into an input value + unit, preferring
// whole hours when the duration is an exact multiple of 60 minutes.
function decodeDuration(ms: number): { value: number; unit: DurationUnit } {
  const minutes = Math.max(0, Math.round(ms / 60000));
  if (minutes > 0 && minutes % 60 === 0) {
    return { value: minutes / 60, unit: "hours" };
  }
  return { value: minutes, unit: "minutes" };
}

function durationToMs(value: number, unit: DurationUnit): number {
  const factor = unit === "hours" ? 3600000 : 60000;
  return Math.max(0, value) * factor;
}

// The repeat interval in ms for a recurrence (used to detect overlap).
function intervalMs(recurrence: Recurrence): number {
  switch (recurrence) {
    case "daily":
      return 24 * 3600000;
    case "weekly":
      return 7 * 24 * 3600000;
    case "monthly":
      return 28 * 24 * 3600000; // shortest month, conservative
    default:
      return Infinity;
  }
}

export function MaintenanceWindowForm({
  mode,
  initialData,
  initialChecks,
  isPending,
  onSubmit,
  onCancel,
}: {
  mode: "create" | "edit";
  initialData?: MaintenanceWindow;
  initialChecks?: MaintenanceWindowFormChecks;
  isPending: boolean;
  onSubmit: (data: MaintenanceWindowFormSubmit) => Promise<void>;
  onCancel: () => void;
}) {
  const { t } = useTranslation("maintenanceWindows");
  // The form is always rendered under the /orgs/$org/* route tree; read the
  // org slug for the check pickers from the router.
  const org = (useParams({ strict: false }) as { org?: string }).org ?? "";

  const [title, setTitle] = useState(initialData?.title ?? "");
  const [description, setDescription] = useState(initialData?.description ?? "");
  const [recurrence, setRecurrence] = useState<Recurrence>(
    initialData?.recurrence ?? "none",
  );

  // --- one-time fields (recurrence === "none") -----------------------------
  const [startAt, setStartAt] = useState(isoToLocalInput(initialData?.startAt));
  const [endAt, setEndAt] = useState(isoToLocalInput(initialData?.endAt));

  // --- recurring fields -----------------------------------------------------
  const initialStart = initialData?.startAt
    ? new Date(initialData.startAt)
    : null;
  const initialDurationMs =
    initialData?.startAt && initialData?.endAt
      ? new Date(initialData.endAt).getTime() -
        new Date(initialData.startAt).getTime()
      : 3600000; // default 1h
  const initialDuration = decodeDuration(initialDurationMs);
  const initialDom = initialStart ? initialStart.getDate() : 1;

  const [startTime, setStartTime] = useState(
    isoToLocalTime(initialData?.startAt) || "22:00",
  );
  const [durationValue, setDurationValue] = useState<string>(
    String(initialDuration.value || 1),
  );
  const [durationUnit, setDurationUnit] = useState<DurationUnit>(
    initialDuration.unit,
  );
  const [weekday, setWeekday] = useState<number>(
    initialStart ? initialStart.getDay() : new Date().getDay(),
  );
  // Day-of-month selector is capped at 28; surface a note when the stored value
  // exceeds it (a window created via the API, before this cap existed).
  const [dayOfMonth, setDayOfMonth] = useState<number>(
    Math.min(initialDom, 28),
  );
  const [firstDay, setFirstDay] = useState(
    isoToLocalDate(initialData?.startAt) || isoToLocalDate(new Date().toISOString()),
  );
  const [until, setUntil] = useState(isoToLocalDate(initialData?.recurrenceEnd));

  const [checkUids, setCheckUids] = useState<string[]>(
    initialChecks?.checkUids ?? [],
  );
  const [checkGroupUids, setCheckGroupUids] = useState<string[]>(
    initialChecks?.checkGroupUids ?? [],
  );
  const [errors, setErrors] = useState<Record<string, string>>({});

  const durationMs = durationToMs(Number(durationValue), durationUnit);

  // Encode the current form state into the API contract. Returns null when the
  // required fields for the chosen recurrence are not yet filled.
  const encode = (): CreateMaintenanceWindowRequest | null => {
    if (recurrence === "none") {
      if (!startAt || !endAt) return null;
      return {
        title: title.trim(),
        description: description.trim() || undefined,
        startAt: localInputToIso(startAt),
        endAt: localInputToIso(endAt),
        recurrence: "none",
        recurrenceEnd: null,
      };
    }

    const base = composeLocalDateTime(firstDay, startTime);
    if (!base) return null;
    const anchor = snapAnchor(base, recurrence, weekday, dayOfMonth);
    const start = anchor.toISOString();
    const end = new Date(anchor.getTime() + durationMs).toISOString();

    let recurrenceEnd: string | null = null;
    if (until) {
      // Encode "Until" as the end of that local day so the last day is inclusive.
      const endOfDay = new Date(`${until}T23:59:59`);
      if (!Number.isNaN(endOfDay.getTime())) {
        recurrenceEnd = endOfDay.toISOString();
      }
    }

    return {
      title: title.trim(),
      description: description.trim() || undefined,
      startAt: start,
      endAt: end,
      recurrence,
      recurrenceEnd,
    };
  };

  // Live draft used to render the summary panel as the user edits.
  const draft = useMemo<MaintenanceWindow | null>(() => {
    const encoded = encode();
    if (!encoded) return null;
    return {
      uid: initialData?.uid ?? "draft",
      title: encoded.title || "—",
      description: encoded.description,
      startAt: encoded.startAt,
      endAt: encoded.endAt,
      recurrence: encoded.recurrence as Recurrence,
      recurrenceEnd: encoded.recurrenceEnd ?? undefined,
      createdAt: initialData?.createdAt ?? new Date().toISOString(),
      updatedAt: initialData?.updatedAt ?? new Date().toISOString(),
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    title,
    description,
    recurrence,
    startAt,
    endAt,
    startTime,
    durationValue,
    durationUnit,
    weekday,
    dayOfMonth,
    firstDay,
    until,
  ]);

  const overlapWarning =
    recurrence !== "none" && durationMs > 0 && durationMs >= intervalMs(recurrence);

  const validate = (): boolean => {
    const next: Record<string, string> = {};
    if (!title.trim()) next.title = t("form.errors.titleRequired");

    if (recurrence === "none") {
      const start = startAt ? new Date(startAt) : null;
      const end = endAt ? new Date(endAt) : null;
      if (start && end && end <= start) {
        next.endAt = t("form.errors.endAfterStart");
      }
    } else {
      if (durationMs <= 0) next.duration = t("form.errors.durationRequired");
      if (!firstDay) next.firstDay = t("form.errors.firstDayRequired");
      if (until && firstDay) {
        if (new Date(`${until}T23:59:59`) <= new Date(`${firstDay}T00:00:00`)) {
          next.until = t("form.errors.untilAfterFirstDay");
        }
      }
    }

    setErrors(next);
    return Object.keys(next).length === 0;
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!validate()) return;
    const window = encode();
    if (!window) return;
    await onSubmit({ window, checkUids, checkGroupUids });
  };

  const canSubmit =
    Boolean(title.trim()) &&
    (recurrence === "none"
      ? Boolean(startAt && endAt)
      : Boolean(firstDay && startTime && durationMs > 0));

  // 1–28 options for the day-of-month select.
  const domOptions = Array.from({ length: 28 }, (_, i) => i + 1);
  // Mon..Sun chips. JS getDay(): 0=Sun..6=Sat. Render Monday-first for locale
  // familiarity, labelling via Intl from a known reference week.
  const weekdayOrder = [1, 2, 3, 4, 5, 6, 0];
  const weekdayLabel = (dow: number) => {
    // 2024-01-01 is a Monday (dow 1); offset to the requested weekday.
    const ref = new Date(2024, 0, 1 + ((dow - 1 + 7) % 7));
    return ref.toLocaleDateString(undefined, { weekday: "short" });
  };

  return (
    <form onSubmit={handleSubmit} className="space-y-6 max-w-2xl">
      <div className="flex items-center gap-4">
        <Button type="button" variant="ghost" size="icon" onClick={onCancel}>
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <h1 className="text-2xl sm:text-3xl font-bold tracking-tight">
          {mode === "create" ? t("form.createTitle") : t("form.editTitle")}
        </h1>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t("form.detailsTitle")}</CardTitle>
          <CardDescription>
            {mode === "create"
              ? t("form.createDescription")
              : t("form.editDescription")}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="mw-title">{t("form.title")}</Label>
            <Input
              id="mw-title"
              data-testid="mw-title-input"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder={t("form.titlePlaceholder")}
              required
              aria-invalid={!!errors.title}
            />
            {errors.title && (
              <p className="text-xs text-destructive">{errors.title}</p>
            )}
          </div>

          <div className="space-y-2">
            <Label htmlFor="mw-description">{t("form.description")}</Label>
            <Textarea
              id="mw-description"
              data-testid="mw-description-input"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder={t("form.descriptionPlaceholder")}
              rows={3}
            />
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("form.scheduleTitle")}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          {/* Recurrence selector (always shown) */}
          <div className="space-y-2">
            <Label htmlFor="mw-recurrence">{t("form.recurrence")}</Label>
            <Select
              value={recurrence}
              onValueChange={(v) => setRecurrence(v as Recurrence)}
            >
              <SelectTrigger
                id="mw-recurrence"
                data-testid="mw-recurrence-select"
                className="sm:max-w-xs"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="none">{t("recurrence.none")}</SelectItem>
                <SelectItem value="daily">{t("recurrence.daily")}</SelectItem>
                <SelectItem value="weekly">{t("recurrence.weekly")}</SelectItem>
                <SelectItem value="monthly">
                  {t("recurrence.monthly")}
                </SelectItem>
              </SelectContent>
            </Select>
          </div>

          {recurrence === "none" ? (
            // One-time: full start/end datetimes are correct here.
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor="mw-start">{t("form.start")}</Label>
                <Input
                  id="mw-start"
                  data-testid="mw-start-input"
                  type="datetime-local"
                  value={startAt}
                  onChange={(e) => setStartAt(e.target.value)}
                  required
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="mw-end">{t("form.end")}</Label>
                <Input
                  id="mw-end"
                  data-testid="mw-end-input"
                  type="datetime-local"
                  value={endAt}
                  onChange={(e) => setEndAt(e.target.value)}
                  required
                  aria-invalid={!!errors.endAt}
                />
                {errors.endAt && (
                  <p className="text-xs text-destructive">{errors.endAt}</p>
                )}
              </div>
            </div>
          ) : (
            <div className="space-y-4">
              {/* Weekday chips (weekly) */}
              {recurrence === "weekly" && (
                <div className="space-y-2">
                  <Label>{t("form.repeatOn")}</Label>
                  <div className="flex flex-wrap gap-2" role="group">
                    {weekdayOrder.map((dow) => (
                      <Button
                        key={dow}
                        type="button"
                        size="sm"
                        variant={weekday === dow ? "default" : "outline"}
                        aria-pressed={weekday === dow}
                        data-testid={`mw-weekday-${dow}`}
                        onClick={() => setWeekday(dow)}
                      >
                        {weekdayLabel(dow)}
                      </Button>
                    ))}
                  </div>
                </div>
              )}

              {/* Day of month (monthly) */}
              {recurrence === "monthly" && (
                <div className="space-y-2 sm:max-w-xs">
                  <Label htmlFor="mw-dayofmonth">{t("form.dayOfMonth")}</Label>
                  <Select
                    value={String(dayOfMonth)}
                    onValueChange={(v) => setDayOfMonth(Number(v))}
                  >
                    <SelectTrigger
                      id="mw-dayofmonth"
                      data-testid="mw-dayofmonth-select"
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {domOptions.map((d) => (
                        <SelectItem key={d} value={String(d)}>
                          {d}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <p className="text-xs text-muted-foreground">
                    {t("form.dayOfMonthHelp")}
                  </p>
                </div>
              )}

              <div className="grid gap-4 sm:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="mw-start-time">{t("form.startTime")}</Label>
                  <Input
                    id="mw-start-time"
                    data-testid="mw-start-time-input"
                    type="time"
                    value={startTime}
                    onChange={(e) => setStartTime(e.target.value)}
                    required
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="mw-duration">{t("form.duration")}</Label>
                  <div className="flex gap-2">
                    <Input
                      id="mw-duration"
                      data-testid="mw-duration-input"
                      type="number"
                      min={1}
                      step={1}
                      value={durationValue}
                      onChange={(e) => setDurationValue(e.target.value)}
                      className="max-w-[7rem]"
                      aria-invalid={!!errors.duration}
                    />
                    <Select
                      value={durationUnit}
                      onValueChange={(v) => setDurationUnit(v as DurationUnit)}
                    >
                      <SelectTrigger
                        data-testid="mw-duration-unit-select"
                        className="flex-1"
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="minutes">
                          {t("form.durationUnitMinutes")}
                        </SelectItem>
                        <SelectItem value="hours">
                          {t("form.durationUnitHours")}
                        </SelectItem>
                      </SelectContent>
                    </Select>
                  </div>
                  {errors.duration && (
                    <p className="text-xs text-destructive">{errors.duration}</p>
                  )}
                </div>
              </div>

              <div className="grid gap-4 sm:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="mw-first-day">{t("form.firstDay")}</Label>
                  <Input
                    id="mw-first-day"
                    data-testid="mw-first-day-input"
                    type="date"
                    value={firstDay}
                    onChange={(e) => setFirstDay(e.target.value)}
                    required
                    aria-invalid={!!errors.firstDay}
                  />
                  {errors.firstDay && (
                    <p className="text-xs text-destructive">{errors.firstDay}</p>
                  )}
                </div>
                <div className="space-y-2">
                  <Label htmlFor="mw-recurrence-end">{t("form.until")}</Label>
                  <Input
                    id="mw-recurrence-end"
                    data-testid="mw-recurrence-end-input"
                    type="date"
                    value={until}
                    onChange={(e) => setUntil(e.target.value)}
                    aria-invalid={!!errors.until}
                  />
                  <p className="text-xs text-muted-foreground">
                    {t("form.untilHelp")}
                  </p>
                  {errors.until && (
                    <p className="text-xs text-destructive">{errors.until}</p>
                  )}
                </div>
              </div>
            </div>
          )}

          {/* Live plain-language summary + next-occurrence preview */}
          {draft && <MaintenanceScheduleSummary window={draft} />}

          {overlapWarning && (
            <p className="flex items-start gap-2 text-xs text-muted-foreground">
              <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-yellow-600 dark:text-yellow-400" />
              {t("form.errors.durationOverlapsWarning")}
            </p>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("form.affectedTitle")}</CardTitle>
          <CardDescription>{t("form.affectedDescription")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label>{t("form.checks")}</Label>
            <CheckMultiPicker
              org={org}
              kind="checks"
              value={checkUids}
              onChange={setCheckUids}
              data-testid="mw-checks-select"
            />
          </div>
          <div className="space-y-2">
            <Label>{t("form.groups")}</Label>
            <CheckMultiPicker
              org={org}
              kind="groups"
              value={checkGroupUids}
              onChange={setCheckGroupUids}
              data-testid="mw-groups-select"
            />
          </div>
        </CardContent>
      </Card>

      <div className="flex gap-3">
        <Button type="button" variant="outline" onClick={onCancel}>
          {t("form.cancel")}
        </Button>
        <Button
          type="submit"
          disabled={isPending || !canSubmit}
          data-testid="mw-submit-button"
        >
          {isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
          {mode === "create" ? t("form.submitCreate") : t("form.submitSave")}
        </Button>
      </div>
    </form>
  );
}
