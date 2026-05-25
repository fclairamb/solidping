import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Trash2, Users } from "lucide-react";
import { toast } from "sonner";
import {
  useStatusPageSubscribers,
  useDeleteStatusPageSubscriber,
} from "@/api/hooks";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
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

interface StatusPageSubscribersProps {
  org: string;
  statusPageUid: string;
}

// StatusPageSubscribers renders a read-only admin list of a status page's
// email subscribers, with a count and a per-row remove action. Subscriber
// emails are PII — managed here only by org admins for their own page.
export function StatusPageSubscribers({
  org,
  statusPageUid,
}: StatusPageSubscribersProps) {
  const { t } = useTranslation("statusPages");
  const { data, isLoading } = useStatusPageSubscribers(org, statusPageUid);
  const deleteSubscriber = useDeleteStatusPageSubscriber(org, statusPageUid);
  const [deleteUid, setDeleteUid] = useState<string | null>(null);

  const subscribers = data?.subscribers ?? [];
  const count = data?.count ?? 0;

  const handleDelete = async () => {
    if (!deleteUid) return;
    try {
      await deleteSubscriber.mutateAsync(deleteUid);
      toast.success(t("subscribers.removed"));
    } catch {
      toast.error(t("subscribers.removeError"));
    } finally {
      setDeleteUid(null);
    }
  };

  return (
    <Card data-testid="status-page-subscribers">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Users className="h-5 w-5" />
          {t("subscribers.title")}
          <Badge variant="secondary" data-testid="subscriber-count">
            {count}
          </Badge>
        </CardTitle>
        <CardDescription>{t("subscribers.description")}</CardDescription>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <div className="space-y-2">
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-full" />
          </div>
        ) : subscribers.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            {t("subscribers.empty")}
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("subscribers.email")}</TableHead>
                <TableHead>{t("subscribers.scope")}</TableHead>
                <TableHead>{t("subscribers.status")}</TableHead>
                <TableHead className="text-right">
                  {t("subscribers.actions")}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {subscribers.map((sub) => (
                <TableRow key={sub.uid} data-testid="subscriber-row">
                  <TableCell className="break-all">{sub.email}</TableCell>
                  <TableCell>
                    <Badge variant="outline">{sub.scope}</Badge>
                  </TableCell>
                  <TableCell>
                    {sub.confirmed ? (
                      <Badge variant="success">
                        {t("subscribers.confirmed")}
                      </Badge>
                    ) : (
                      <Badge variant="secondary">
                        {t("subscribers.pending")}
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center justify-end">
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8 text-destructive hover:text-destructive"
                        onClick={() => setDeleteUid(sub.uid)}
                        aria-label={t("subscribers.remove")}
                        data-testid="subscriber-row-delete"
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>

      <AlertDialog
        open={!!deleteUid}
        onOpenChange={(o) => !o && setDeleteUid(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("subscribers.removeConfirmTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("subscribers.removeConfirmDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("subscribers.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDelete}
              disabled={deleteSubscriber.isPending}
            >
              {t("subscribers.remove")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  );
}
