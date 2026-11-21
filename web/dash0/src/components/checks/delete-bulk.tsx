import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";

import { useDeleteSelectedChecks, useDeleteSelectedChecksPreview } from "@/api/hooks";
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

/** Above this many checks, the count has to be typed to confirm. */
export const TYPED_CONFIRMATION_THRESHOLD = 10;

/**
 * Deletes the explicitly selected checks (spec 2026-11-21-03). Opening it
 * dry-runs the delete so the counts, and the checks that will be left in place
 * with the reason, are the server's.
 */
export function DeleteBulkDialog({
  org,
  checkUids,
  open,
  onOpenChange,
  onDeleted,
}: {
  org: string;
  checkUids: string[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDeleted: (deleted: string[]) => void;
}) {
  const { t } = useTranslation("checks");
  const { t: tc } = useTranslation("common");
  const [typed, setTyped] = useState("");

  const preview = useDeleteSelectedChecksPreview(org, checkUids, open);
  const deleteChecks = useDeleteSelectedChecks(org);

  const deletable = preview.data?.deleted ?? [];
  const skipped = preview.data?.skipped ?? [];
  const needsTyping = deletable.length > TYPED_CONFIRMATION_THRESHOLD;
  const typedOk = !needsTyping || typed.trim() === String(deletable.length);

  const handleOpenChange = (next: boolean) => {
    if (!next) {
      setTyped("");
    }
    onOpenChange(next);
  };

  const confirm = () => {
    deleteChecks.mutate(
      { checkUids: deletable },
      {
        onSuccess: (result) => {
          toast.success(t("bulkSelection.bulk.deleted", { count: result.deleted.length }));
          if (result.skipped.length > 0) {
            toast.error(t("bulkSelection.bulk.partial", { count: result.skipped.length }));
          }
          setTyped("");
          onDeleted(result.deleted);
          onOpenChange(false);
        },
        onError: () => {
          toast.error(t("bulkSelection.bulk.failed"));
        },
      },
    );
  };

  return (
    <AlertDialog open={open} onOpenChange={handleOpenChange}>
      <AlertDialogContent data-testid="checks-bulk-delete-dialog">
        <AlertDialogHeader>
          <AlertDialogTitle>
            {t("bulkSelection.bulk.deleteTitle", { count: checkUids.length })}
          </AlertDialogTitle>
          <AlertDialogDescription>{t("bulkSelection.bulk.deleteDescription")}</AlertDialogDescription>
        </AlertDialogHeader>
        <div className="space-y-2 text-sm">
          {preview.isLoading ? (
            <Loader2 className="h-4 w-4 animate-spin" />
          ) : (
            <>
              <p data-testid="checks-bulk-delete-summary">
                {t("bulkSelection.bulk.willDelete", { count: deletable.length })}
              </p>
              {skipped.length > 0 && (
                <div data-testid="checks-bulk-delete-skipped">
                  <p>{t("bulkSelection.bulk.skippedIntro", { count: skipped.length })}</p>
                  <ul className="max-h-40 list-disc overflow-y-auto pl-5 text-muted-foreground">
                    {skipped.map((skip) => (
                      <li key={skip.checkUid} data-testid="checks-bulk-delete-skipped-item">
                        {skip.checkUid.slice(0, 8)}: {t(`bulkSelection.bulk.skip.${skip.code}`, { defaultValue: skip.code })}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
              {needsTyping && (
                <div className="space-y-1">
                  <p>{t("bulkSelection.bulk.typeCount", { count: deletable.length })}</p>
                  <Input
                    value={typed}
                    onChange={(event) => setTyped(event.target.value)}
                    data-testid="checks-bulk-delete-confirm-input"
                  />
                </div>
              )}
            </>
          )}
        </div>
        <AlertDialogFooter>
          <AlertDialogCancel>{tc("cancel")}</AlertDialogCancel>
          <AlertDialogAction
            onClick={(event) => {
              event.preventDefault();
              confirm();
            }}
            disabled={deleteChecks.isPending || preview.isLoading || deletable.length === 0 || !typedOk}
            data-testid="checks-bulk-delete-confirm"
          >
            {deleteChecks.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
            {t("bulkSelection.bulk.confirm")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
