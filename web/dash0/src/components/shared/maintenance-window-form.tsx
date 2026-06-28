import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useParams } from "@tanstack/react-router";
import { ArrowLeft, Loader2 } from "lucide-react";

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

type Recurrence = "none" | "daily" | "weekly" | "monthly";

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
  const [startAt, setStartAt] = useState(isoToLocalInput(initialData?.startAt));
  const [endAt, setEndAt] = useState(isoToLocalInput(initialData?.endAt));
  const [recurrence, setRecurrence] = useState<Recurrence>(
    initialData?.recurrence ?? "none",
  );
  const [recurrenceEnd, setRecurrenceEnd] = useState(
    isoToLocalInput(initialData?.recurrenceEnd),
  );
  const [checkUids, setCheckUids] = useState<string[]>(
    initialChecks?.checkUids ?? [],
  );
  const [checkGroupUids, setCheckGroupUids] = useState<string[]>(
    initialChecks?.checkGroupUids ?? [],
  );
  const [errors, setErrors] = useState<Record<string, string>>({});

  const validate = (): boolean => {
    const next: Record<string, string> = {};
    if (!title.trim()) next.title = t("form.errors.titleRequired");
    const start = startAt ? new Date(startAt) : null;
    const end = endAt ? new Date(endAt) : null;
    if (start && end && end <= start) {
      next.endAt = t("form.errors.endAfterStart");
    }
    if (recurrence !== "none" && recurrenceEnd && start) {
      if (new Date(recurrenceEnd) <= start) {
        next.recurrenceEnd = t("form.errors.recurrenceEndAfterStart");
      }
    }
    setErrors(next);
    return Object.keys(next).length === 0;
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!validate()) return;
    await onSubmit({
      window: {
        title: title.trim(),
        description: description.trim() || undefined,
        startAt: localInputToIso(startAt),
        endAt: localInputToIso(endAt),
        recurrence,
        recurrenceEnd:
          recurrence !== "none" && recurrenceEnd
            ? localInputToIso(recurrenceEnd)
            : null,
      },
      checkUids,
      checkGroupUids,
    });
  };

  const canSubmit = Boolean(title.trim() && startAt && endAt);

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

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="mw-recurrence">{t("form.recurrence")}</Label>
              <Select
                value={recurrence}
                onValueChange={(v) => setRecurrence(v as Recurrence)}
              >
                <SelectTrigger id="mw-recurrence" data-testid="mw-recurrence-select">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="none">{t("recurrence.none")}</SelectItem>
                  <SelectItem value="daily">{t("recurrence.daily")}</SelectItem>
                  <SelectItem value="weekly">{t("recurrence.weekly")}</SelectItem>
                  <SelectItem value="monthly">{t("recurrence.monthly")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            {recurrence !== "none" && (
              <div className="space-y-2">
                <Label htmlFor="mw-recurrence-end">{t("form.recurrenceEnd")}</Label>
                <Input
                  id="mw-recurrence-end"
                  data-testid="mw-recurrence-end-input"
                  type="datetime-local"
                  value={recurrenceEnd}
                  onChange={(e) => setRecurrenceEnd(e.target.value)}
                  aria-invalid={!!errors.recurrenceEnd}
                />
                <p className="text-xs text-muted-foreground">
                  {t("form.recurrenceEndHelp")}
                </p>
                {errors.recurrenceEnd && (
                  <p className="text-xs text-destructive">{errors.recurrenceEnd}</p>
                )}
              </div>
            )}
          </div>
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
        <Button type="submit" disabled={isPending || !canSubmit} data-testid="mw-submit-button">
          {isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
          {mode === "create" ? t("form.submitCreate") : t("form.submitSave")}
        </Button>
      </div>
    </form>
  );
}
