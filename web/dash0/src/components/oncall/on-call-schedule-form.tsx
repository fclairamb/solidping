import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Command } from "cmdk";
import { Check, ChevronsUpDown, Save } from "lucide-react";

import { useMembers, type OnCallRotationType } from "@/api/hooks";
import {
  EmailOnlyBadge,
  useEmailOnlyUserUids,
} from "@/components/notifications/member-coverage";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";

export interface OnCallScheduleFormValues {
  name: string;
  description: string;
  timezone: string;
  rotationType: OnCallRotationType;
  handoffTime: string;
  handoffWeekday: number;
  startAt: string;
  userUids: string[];
}

interface Props {
  mode: "create" | "edit";
  org: string;
  initialValues?: Partial<OnCallScheduleFormValues>;
  onSubmit: (values: OnCallScheduleFormValues) => Promise<void> | void;
  onCancel: () => void;
  submitting?: boolean;
  error?: string | null;
}

// Default-from-browser timezone is the v1 simplification — orgs don't carry a
// timezone yet. Falls back to UTC if Intl is unavailable for any reason.
function defaultBrowserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

function nextMondayAt9amISO(): string {
  const now = new Date();
  const dayDiff = (8 - now.getDay()) % 7 || 7;
  const mon = new Date(now);
  mon.setDate(now.getDate() + dayDiff);
  mon.setHours(9, 0, 0, 0);
  return mon.toISOString();
}

function buildDefaults(): OnCallScheduleFormValues {
  return {
    name: "",
    description: "",
    timezone: defaultBrowserTimezone(),
    rotationType: "weekly",
    handoffTime: "09:00",
    handoffWeekday: 0,
    startAt: nextMondayAt9amISO(),
    userUids: [],
  };
}

export function OnCallScheduleForm({
  mode,
  org,
  initialValues,
  onSubmit,
  onCancel,
  submitting,
  error,
}: Props) {
  const { t } = useTranslation(["oncall", "common"]);

  const [values, setValues] = useState<OnCallScheduleFormValues>(() => ({
    ...buildDefaults(),
    ...(initialValues ?? {}),
  }));

  // When editing, the schedule data may load after first render — re-seed
  // the form once the parent passes a hydrated `initialValues`.
  useEffect(() => {
    if (mode !== "edit" || !initialValues) return;
    setValues((prev) => ({ ...prev, ...initialValues }));
  }, [mode, initialValues]);

  const { data: membersResp } = useMembers(org);
  const members = useMemo(() => membersResp?.data ?? [], [membersResp]);
  // Rostering somebody who can only be reached by email is the failure this
  // badge exists to prevent — the silent email fallback in the escalation job
  // makes it look like paging works right up until nobody wakes up.
  const emailOnlyUsers = useEmailOnlyUserUids(org);
  const browserTimezone = useMemo(() => defaultBrowserTimezone(), []);

  const set = <K extends keyof OnCallScheduleFormValues>(
    key: K,
    next: OnCallScheduleFormValues[K],
  ) => setValues((prev) => ({ ...prev, [key]: next }));

  const toggleUser = (uid: string) => {
    setValues((prev) => ({
      ...prev,
      userUids: prev.userUids.includes(uid)
        ? prev.userUids.filter((u) => u !== uid)
        : [...prev.userUids, uid],
    }));
  };

  const handleSubmit = (event: React.FormEvent) => {
    event.preventDefault();
    void onSubmit({
      ...values,
      // Strip handoffWeekday for daily rotations — the backend rejects it.
      handoffWeekday:
        values.rotationType === "weekly" ? values.handoffWeekday : 0,
    });
  };

  const isEdit = mode === "edit";

  return (
    <form className="space-y-4" onSubmit={handleSubmit}>
      <div className="space-y-2">
        <Label htmlFor="oncall-name">{t("oncall:form.name")}</Label>
        <Input
          id="oncall-name"
          value={values.name}
          onChange={(e) => set("name", e.target.value)}
          placeholder={t("oncall:form.namePlaceholder")}
          required
        />
      </div>

      <div className="space-y-2">
        <Label htmlFor="oncall-desc">{t("oncall:form.description")}</Label>
        <Textarea
          id="oncall-desc"
          value={values.description}
          onChange={(e) => set("description", e.target.value)}
          placeholder={t("oncall:form.descriptionPlaceholder")}
        />
      </div>

      <div className="space-y-2">
        <div className="flex items-center justify-between gap-2">
          <Label htmlFor="oncall-tz">{t("oncall:form.timezone")}</Label>
          {browserTimezone && browserTimezone !== values.timezone && (
            <button
              type="button"
              onClick={() => set("timezone", browserTimezone)}
              className="text-xs text-muted-foreground hover:text-foreground hover:underline"
              data-testid="oncall-timezone-use-browser"
            >
              {t("oncall:form.timezoneUseBrowser", { tz: browserTimezone })}
            </button>
          )}
        </div>
        <TimezoneCombobox
          id="oncall-tz"
          value={values.timezone}
          onChange={(v) => set("timezone", v)}
          placeholder={t("oncall:form.timezonePlaceholder")}
          searchPlaceholder={t("oncall:form.timezoneSearch")}
          emptyLabel={t("oncall:form.noTimezones")}
        />
      </div>

      <div className="grid grid-cols-2 gap-4">
        <div className="space-y-2">
          <Label>{t("oncall:form.rotationType")}</Label>
          <Select
            value={values.rotationType}
            onValueChange={(v) => set("rotationType", v as OnCallRotationType)}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="daily">{t("oncall:detail.rotationDaily")}</SelectItem>
              <SelectItem value="weekly">{t("oncall:detail.rotationWeekly")}</SelectItem>
            </SelectContent>
          </Select>
        </div>

        <div className="space-y-2">
          <Label htmlFor="oncall-hot">{t("oncall:form.handoffTime")}</Label>
          <Input
            id="oncall-hot"
            value={values.handoffTime}
            onChange={(e) => set("handoffTime", e.target.value)}
            placeholder={t("oncall:form.handoffTimePlaceholder")}
            pattern="[0-2][0-9]:[0-5][0-9]"
            required
          />
        </div>
      </div>

      {values.rotationType === "weekly" && (
        <div className="space-y-2">
          <Label>{t("oncall:form.handoffWeekday")}</Label>
          <Select
            value={String(values.handoffWeekday)}
            onValueChange={(v) => set("handoffWeekday", Number(v))}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {(["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"] as const).map(
                (day, idx) => (
                  <SelectItem key={day} value={String(idx)}>
                    {t(`oncall:form.weekday${day}`)}
                  </SelectItem>
                ),
              )}
            </SelectContent>
          </Select>
        </div>
      )}

      <div className="space-y-2">
        <Label htmlFor="oncall-start">{t("oncall:form.startAt")}</Label>
        <Input
          id="oncall-start"
          value={values.startAt}
          onChange={(e) => set("startAt", e.target.value)}
          required
        />
        <p className="text-xs text-muted-foreground">
          {t("oncall:form.startAtHelp")}
        </p>
        {isEdit && (
          <p className="text-xs text-amber-600 dark:text-amber-400">
            {t("oncall:form.startAtWarning")}
          </p>
        )}
      </div>

      <div className="space-y-2">
        <Label>{t("oncall:form.userUids")}</Label>
        <p className="text-xs text-muted-foreground">
          {t("oncall:form.userUidsHelp")}
        </p>
        <div className="border rounded p-2 max-h-64 overflow-y-auto">
          {members.length === 0 ? (
            <p className="text-sm text-muted-foreground py-2">
              {t("oncall:detail.noUsers")}
            </p>
          ) : (
            members.map((m) => (
              <label
                key={m.userUid}
                className="flex items-center gap-2 py-1 cursor-pointer"
              >
                <input
                  type="checkbox"
                  checked={values.userUids.includes(m.userUid)}
                  onChange={() => toggleUser(m.userUid)}
                />
                <span className="flex flex-wrap items-center gap-2 text-sm">
                  <span>
                    {m.name || m.email}
                    {m.name ? (
                      <span className="text-muted-foreground"> ({m.email})</span>
                    ) : null}
                  </span>
                  {emailOnlyUsers.has(m.userUid) && <EmailOnlyBadge />}
                </span>
              </label>
            ))
          )}
        </div>
      </div>

      {error && (
        <p className="text-sm text-destructive" role="alert">
          {error}
        </p>
      )}

      <div className="flex justify-end gap-2">
        <Button type="button" variant="outline" onClick={onCancel}>
          {t("oncall:form.cancel")}
        </Button>
        <Button
          type="submit"
          disabled={submitting}
          aria-label={t("oncall:form.submit")}
        >
          <Save />
          <span className="hidden sm:inline">{t("oncall:form.submit")}</span>
        </Button>
      </div>
    </form>
  );
}

const TIMEZONE_FALLBACK = ["UTC"];

function getTimezones(): string[] {
  const intl = Intl as typeof Intl & {
    supportedValuesOf?: (k: string) => string[];
  };
  try {
    const list = intl.supportedValuesOf?.("timeZone");
    if (list && list.length > 0) return list;
  } catch {
    // fall through to fallback
  }
  return TIMEZONE_FALLBACK;
}

interface TimezoneComboboxProps {
  id?: string;
  value: string;
  onChange: (next: string) => void;
  placeholder: string;
  searchPlaceholder: string;
  emptyLabel: string;
}

function TimezoneCombobox({
  id,
  value,
  onChange,
  placeholder,
  searchPlaceholder,
  emptyLabel,
}: TimezoneComboboxProps) {
  const [open, setOpen] = useState(false);
  const timezones = useMemo(() => getTimezones(), []);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          id={id}
          type="button"
          variant="outline"
          role="combobox"
          aria-expanded={open}
          className="w-full justify-between font-normal"
          data-testid="oncall-timezone-select"
        >
          <span className={cn(!value && "text-muted-foreground")}>
            {value || placeholder}
          </span>
          <ChevronsUpDown className="ml-2 h-4 w-4 shrink-0 opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent
        className="w-[--radix-popover-trigger-width] p-0"
        align="start"
      >
        <Command>
          <Command.Input
            placeholder={searchPlaceholder}
            className="flex h-9 w-full border-b bg-transparent px-3 py-2 text-sm outline-none placeholder:text-muted-foreground"
          />
          <Command.List className="max-h-64 overflow-y-auto p-1">
            <Command.Empty className="px-3 py-6 text-center text-sm text-muted-foreground">
              {emptyLabel}
            </Command.Empty>
            {timezones.map((tz) => (
              <Command.Item
                key={tz}
                value={tz}
                onSelect={() => {
                  onChange(tz);
                  setOpen(false);
                }}
                className="flex cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 text-sm aria-selected:bg-accent aria-selected:text-accent-foreground"
              >
                <Check
                  className={cn(
                    "h-4 w-4 shrink-0",
                    value === tz ? "opacity-100" : "opacity-0",
                  )}
                />
                <span>{tz}</span>
              </Command.Item>
            ))}
          </Command.List>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
