import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";

import { useBulkUpdateChecks, useBulkUpdateChecksPreview } from "@/api/hooks";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { formatPeriod, type PeriodUnit } from "@/components/shared/check-form";

const UNITS: { value: PeriodUnit; label: string }[] = [
  { value: "minutes", label: "unitMinutes" },
  { value: "hours", label: "unitHours" },
  { value: "days", label: "unitDays" },
  { value: "weeks", label: "unitWeeks" },
];

/**
 * The selection bar's "Set period" (a controlled dialog): gives every
 * selected check the same period through the bulk check PATCH. The dry run
 * states how many the period suits (a check type may have a longer minimum)
 * before anything is written.
 */
export function SetPeriodBulkDialog({
  org,
  checkUids,
  open,
  onOpenChange,
}: {
  org: string;
  checkUids: string[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useTranslation("checks");
  const { t: tc } = useTranslation("common");
  const [value, setValue] = useState(5);
  const [unit, setUnit] = useState<PeriodUnit>("minutes");

  const changes = useMemo(() => ({ period: formatPeriod(value, unit) }), [value, unit]);
  const preview = useBulkUpdateChecksPreview(org, checkUids, changes, open);
  const setPeriod = useBulkUpdateChecks(org);

  const updatable = preview.data?.data.length ?? 0;
  const skipped = preview.data?.skipped.length ?? 0;

  const confirm = () => {
    setPeriod.mutate(
      { checkUids, changes },
      {
        onSuccess: (result) => {
          toast.success(t("bulkSelection.period.updated", { count: result.data.length }));
          onOpenChange(false);
        },
        onError: () => {
          toast.error(t("bulkSelection.period.failed"));
        },
      },
    );
  };

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent data-testid="set-period-dialog">
        <AlertDialogHeader>
          <AlertDialogTitle>{t("bulkSelection.period.title", { count: checkUids.length })}</AlertDialogTitle>
          <AlertDialogDescription>{t("bulkSelection.period.pick")}</AlertDialogDescription>
        </AlertDialogHeader>
        <div className="flex gap-2">
          <Input
            type="number"
            min={1}
            value={value}
            onChange={(event) => setValue(parseInt(event.target.value, 10) || 1)}
            className="w-24"
            data-testid="set-period-input"
          />
          <Select value={unit} onValueChange={(next) => setUnit(next as PeriodUnit)}>
            <SelectTrigger className="flex-1" data-testid="set-period-unit-select">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {UNITS.map((item) => (
                <SelectItem key={item.value} value={item.value}>
                  {t(`form.${item.label}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <p className="text-sm" data-testid="set-period-preview">
          {preview.isLoading ? (
            <Loader2 className="h-4 w-4 animate-spin" />
          ) : (
            <>
              {t("bulkSelection.period.willUpdate", { count: updatable })}
              {skipped > 0 && <> {t("bulkSelection.period.willSkip", { count: skipped })}</>}
            </>
          )}
        </p>
        <AlertDialogFooter>
          <AlertDialogCancel>{tc("cancel")}</AlertDialogCancel>
          <AlertDialogAction
            onClick={(event) => {
              event.preventDefault();
              confirm();
            }}
            disabled={setPeriod.isPending || updatable === 0}
            data-testid="set-period-confirm"
          >
            {setPeriod.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
            {t("bulkSelection.period.confirm")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
