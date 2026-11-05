import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";

import {
  useCheckUidsMatching,
  useRegions,
  useSetRegions,
  useSetRegionsPreview,
  type BulkCheckFilters,
} from "@/api/hooks";
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
import { Checkbox } from "@/components/ui/checkbox";

/**
 * The checks list's bulk action "Set regions…" (spec 2026-11-05-01), a
 * controlled dialog opened from the header ⋯ menu. Its scope is the checks the
 * list's filters currently match; the dialog states that count, dry-runs the
 * pin for the chosen regions, then pins them all on confirm.
 */
export function SetRegionsBulkDialog({
  org,
  filters,
  open,
  onOpenChange,
}: {
  org: string;
  filters: BulkCheckFilters;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useTranslation("checks");
  const { t: tc } = useTranslation("common");
  const [selected, setSelected] = useState<string[]>([]);

  const { data: regionsData } = useRegions(org);
  const matching = useCheckUidsMatching(org, filters, open);
  const uids = matching.data ?? [];
  const preview = useSetRegionsPreview(org, uids, selected, open);
  const setRegions = useSetRegions(org);

  const toggle = (slug: string) =>
    setSelected((current) =>
      current.includes(slug) ? current.filter((s) => s !== slug) : [...current, slug],
    );

  const updatable = preview.data?.updated.length ?? 0;
  const skipped = preview.data?.skipped.length ?? 0;

  const confirm = () => {
    setRegions.mutate(
      { checkUids: uids, regions: selected },
      {
        onSuccess: (result) => {
          toast.success(t("setRegions.updated", { count: result.updated.length }));
          onOpenChange(false);
        },
        onError: () => {
          toast.error(t("setRegions.failed"));
        },
      },
    );
  };

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent data-testid="set-regions-dialog">
        <AlertDialogHeader>
          <AlertDialogTitle>{t("setRegions.title")}</AlertDialogTitle>
          <AlertDialogDescription data-testid="set-regions-scope">
            {matching.isLoading ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : uids.length > 0 ? (
              t("setRegions.scope", { count: uids.length })
            ) : (
              t("setRegions.scopeNone")
            )}
          </AlertDialogDescription>
        </AlertDialogHeader>
        {uids.length > 0 && (
          <div className="space-y-2">
            <p className="text-sm text-muted-foreground">{t("setRegions.pick")}</p>
            <div className="max-h-64 space-y-1 overflow-y-auto" data-testid="set-regions-options">
              {(regionsData?.regions ?? []).map((region) => (
                <label
                  key={region.slug}
                  className="flex cursor-pointer items-center gap-2 rounded-md border p-2 hover:bg-muted/50"
                  data-testid={`set-regions-option-${region.slug}`}
                >
                  <Checkbox
                    checked={selected.includes(region.slug)}
                    onCheckedChange={() => toggle(region.slug)}
                    data-testid={`set-regions-checkbox-${region.slug}`}
                  />
                  <span className="text-sm">
                    {region.emoji} {region.name}
                  </span>
                </label>
              ))}
            </div>
            {selected.length > 0 && (
              <p className="text-sm" data-testid="set-regions-preview">
                {preview.isLoading ? (
                  <Loader2 className="h-4 w-4 animate-spin" />
                ) : (
                  <>
                    {t("setRegions.preview", { count: updatable })}
                    {skipped > 0 && <> {t("setRegions.previewSkipped", { count: skipped })}</>}
                  </>
                )}
              </p>
            )}
          </div>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel>{tc("cancel")}</AlertDialogCancel>
          <AlertDialogAction
            onClick={(event) => {
              event.preventDefault();
              confirm();
            }}
            disabled={setRegions.isPending || selected.length === 0 || updatable === 0}
            data-testid="set-regions-confirm"
          >
            {setRegions.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
            {t("setRegions.confirm")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
