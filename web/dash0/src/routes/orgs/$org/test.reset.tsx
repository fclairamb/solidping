import { createFileRoute } from "@tanstack/react-router";
import { Trans, useTranslation } from "react-i18next";
import { toast } from "sonner";
import { Loader2, Trash2 } from "lucide-react";
import { useDeleteAllChecks } from "@/api/hooks";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";

export const Route = createFileRoute("/orgs/$org/test/reset")({
  component: ResetTab,
});

function ResetTab() {
  const { t } = useTranslation(["nav", "common"]);
  const { org } = Route.useParams();
  const deleteAllChecks = useDeleteAllChecks();

  const handleDeleteAll = async () => {
    try {
      const result = await deleteAllChecks.mutateAsync(org);
      if (result.failed === 0) {
        toast.success(t("nav:test.reset.deletedCount", { count: result.deleted }));
      } else {
        toast.warning(
          t("nav:test.reset.deletedWithFailures", {
            deleted: result.deleted,
            failed: result.failed,
          }),
        );
      }
    } catch (err) {
      const message =
        err instanceof Error ? err.message : t("nav:test.reset.deleteFailed");
      toast.error(message);
    }
  };

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("nav:test.reset.title")}</CardTitle>
          <CardDescription>
            <Trans
              i18nKey="nav:test.reset.description"
              values={{ org }}
              components={{ strong: <code className="text-xs" /> }}
            />
          </CardDescription>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">{t("nav:test.reset.warning")}</p>
        </CardContent>
        <CardFooter>
          <AlertDialog>
            <AlertDialogTrigger asChild>
              <Button
                variant="destructive"
                disabled={deleteAllChecks.isPending}
              >
                {deleteAllChecks.isPending ? (
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                ) : (
                  <Trash2 className="mr-2 h-4 w-4" />
                )}
                {t("nav:test.reset.button")}
              </Button>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>{t("nav:test.reset.confirmTitle")}</AlertDialogTitle>
                <AlertDialogDescription>
                  <Trans
                    i18nKey="nav:test.reset.confirmDescription"
                    values={{ org }}
                    components={{ strong: <strong /> }}
                  />
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>{t("common:cancel")}</AlertDialogCancel>
                <AlertDialogAction
                  onClick={handleDeleteAll}
                  className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
                >
                  {t("nav:test.reset.confirmAction")}
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        </CardFooter>
      </Card>
    </div>
  );
}
