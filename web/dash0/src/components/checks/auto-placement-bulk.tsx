import { useMemo } from "react";
import { useTranslation } from "react-i18next";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";

import { useBulkUpdateChecks, useBulkUpdateChecksPreview, useChecksMatching } from "@/api/hooks";
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

const AUTO_PLACEMENT = { placement: "auto" } as const;

/**
 * The checks list's bulk action (a controlled dialog, opened from the header ⋯ menu) "Switch to automatic placement" (spec
 * 2026-09-25-06). Opening it dry-runs `{placement: "auto"}` on the pinned checks
 * through the bulk check PATCH (spec 2026-11-05-01), so the dialog states how
 * many it would touch; confirming switches them. Each keeps its regions and
 * region count, so nothing about cost or where it runs today changes — it only
 * gains failover when one of its regions goes dark.
 */
export function AutoPlacementBulkDialog({
  org,
  open,
  onOpenChange,
}: {
  org: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useTranslation("checks");
  const { t: tc } = useTranslation("common");
  const setOpen = onOpenChange;
  const matching = useChecksMatching(org, {}, open);
  const pinned = useMemo(
    () => (matching.data ?? []).filter((check) => check.placement === "pinned").map((check) => check.uid),
    [matching.data],
  );
  const preview = useBulkUpdateChecksPreview(org, pinned, AUTO_PLACEMENT, open);
  const switchChecks = useBulkUpdateChecks(org);

  const eligible = preview.data?.data.map((check) => check.uid) ?? [];
  const count = eligible.length;
  const loading = matching.isLoading || preview.isLoading;

  const confirm = () => {
    switchChecks.mutate({ checkUids: eligible, changes: AUTO_PLACEMENT }, {
      onSuccess: (result) => {
        toast.success(t("autoPlacement.switched", { count: result.data.length }));
        setOpen(false);
      },
      onError: () => {
        toast.error(t("autoPlacement.failed"));
      },
    });
  };

  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      <AlertDialogContent data-testid="auto-placement-dialog">
        <AlertDialogHeader>
          <AlertDialogTitle>{t("autoPlacement.title")}</AlertDialogTitle>
          <AlertDialogDescription data-testid="auto-placement-description">
            {loading ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : count > 0 ? (
              t("autoPlacement.description", { count })
            ) : (
              t("autoPlacement.none")
            )}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{tc("cancel")}</AlertDialogCancel>
          {count > 0 && (
            <AlertDialogAction
              onClick={(event) => {
                event.preventDefault();
                confirm();
              }}
              disabled={switchChecks.isPending}
              data-testid="auto-placement-confirm"
            >
              {switchChecks.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
              {t("autoPlacement.confirm")}
            </AlertDialogAction>
          )}
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
