import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2, Shuffle } from "lucide-react";
import { toast } from "sonner";

import { useAutoPlacementPreview, useSwitchToAutoPlacement } from "@/api/hooks";
import { Button } from "@/components/ui/button";
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

/**
 * The checks list's bulk action "Switch to automatic placement" (spec
 * 2026-09-25-06). Opening it dry-runs the switch so the dialog states how many
 * checks it would touch; confirming switches them. Each keeps its regions and
 * region count, so nothing about cost or where it runs today changes — it only
 * gains failover when one of its regions goes dark.
 */
export function AutoPlacementBulkButton({ org }: { org: string }) {
  const { t } = useTranslation("checks");
  const { t: tc } = useTranslation("common");
  const [open, setOpen] = useState(false);
  const preview = useAutoPlacementPreview(org, open);
  const switchChecks = useSwitchToAutoPlacement(org);

  const count = preview.data?.data.length ?? 0;

  const confirm = () => {
    switchChecks.mutate(undefined, {
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
    <>
      <Button
        variant="outline"
        onClick={() => setOpen(true)}
        data-testid="auto-placement-button"
        aria-label={t("autoPlacement.title")}
      >
        <Shuffle className="sm:mr-2 h-4 w-4" />
        <span className="hidden sm:inline">{t("autoPlacement.button")}</span>
      </Button>
      <AlertDialog open={open} onOpenChange={setOpen}>
        <AlertDialogContent data-testid="auto-placement-dialog">
          <AlertDialogHeader>
            <AlertDialogTitle>{t("autoPlacement.title")}</AlertDialogTitle>
            <AlertDialogDescription data-testid="auto-placement-description">
              {preview.isLoading ? (
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
    </>
  );
}
