import { createFileRoute } from "@tanstack/react-router";
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
  const { org } = Route.useParams();
  const deleteAllChecks = useDeleteAllChecks();

  const handleDeleteAll = async () => {
    try {
      const result = await deleteAllChecks.mutateAsync(org);
      if (result.failed === 0) {
        toast.success(`Deleted ${result.deleted} checks`);
      } else {
        toast.warning(
          `Deleted ${result.deleted}, failed ${result.failed} checks`,
        );
      }
    } catch (err) {
      const message =
        err instanceof Error ? err.message : "Failed to delete checks";
      toast.error(message);
    }
  };

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Delete all checks</CardTitle>
          <CardDescription>
            Remove every check and its associated results from the organization{" "}
            <code className="text-xs">{org}</code>. This cannot be undone.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            This will permanently delete all checks, their results, incidents,
            and check jobs for this organization.
          </p>
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
                Delete all checks
              </Button>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>Delete all checks?</AlertDialogTitle>
                <AlertDialogDescription>
                  This will permanently delete every check in the{" "}
                  <strong>{org}</strong> organization along with all associated
                  data. This action cannot be undone.
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>Cancel</AlertDialogCancel>
                <AlertDialogAction
                  onClick={handleDeleteAll}
                  className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
                >
                  Delete all
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        </CardFooter>
      </Card>
    </div>
  );
}
