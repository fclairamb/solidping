import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2 } from "lucide-react";
import { toast } from "sonner";

import { useBulkUpdateChecks, type CheckGroup } from "@/api/hooks";
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

const NO_GROUP = "none";

/**
 * The selection bar's "Move to group" (a controlled dialog): puts every
 * selected check into the chosen group, or takes them out of their group,
 * through the bulk check PATCH.
 */
export function MoveGroupBulkDialog({
  org,
  checkUids,
  groups,
  open,
  onOpenChange,
}: {
  org: string;
  checkUids: string[];
  groups: CheckGroup[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { t } = useTranslation("checks");
  const { t: tc } = useTranslation("common");
  const [target, setTarget] = useState<string>("");
  const move = useBulkUpdateChecks(org);

  const confirm = () => {
    move.mutate(
      { checkUids, changes: { checkGroupUid: target === NO_GROUP ? "" : target } },
      {
        onSuccess: (result) => {
          toast.success(t("bulkSelection.move.moved", { count: result.data.length }));
          onOpenChange(false);
        },
        onError: () => {
          toast.error(t("bulkSelection.move.failed"));
        },
      },
    );
  };

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent data-testid="move-group-dialog">
        <AlertDialogHeader>
          <AlertDialogTitle>{t("bulkSelection.move.title", { count: checkUids.length })}</AlertDialogTitle>
          <AlertDialogDescription>{t("bulkSelection.move.pick")}</AlertDialogDescription>
        </AlertDialogHeader>
        <Select value={target} onValueChange={setTarget}>
          <SelectTrigger data-testid="move-group-select">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={NO_GROUP} data-testid="move-group-option-none">
              {t("bulkSelection.move.none")}
            </SelectItem>
            {groups.map((group) => (
              <SelectItem key={group.uid} value={group.uid} data-testid={`move-group-option-${group.slug}`}>
                {group.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <AlertDialogFooter>
          <AlertDialogCancel>{tc("cancel")}</AlertDialogCancel>
          <AlertDialogAction
            onClick={(event) => {
              event.preventDefault();
              confirm();
            }}
            disabled={move.isPending || target === ""}
            data-testid="move-group-confirm"
          >
            {move.isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
            {t("bulkSelection.move.confirm")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
