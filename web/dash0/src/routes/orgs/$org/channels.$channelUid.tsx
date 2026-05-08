import { useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, useNavigate, Link } from "@tanstack/react-router";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  CardDescription,
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
} from "@/components/ui/alert-dialog";
import { Loader2, Trash2 } from "lucide-react";
import { toast } from "sonner";
import {
  useChannel,
  useUpdateChannel,
  useDeleteChannel,
} from "@/api/hooks";
import { ChannelIcon, channelLabel } from "@/components/channels/channel-icon";
import {
  ChannelForm,
  type ChannelFormState,
} from "@/components/channels/channel-form";

export const Route = createFileRoute("/orgs/$org/channels/$channelUid")({
  component: ChannelDetailPage,
});

function ChannelDetailPage() {
  const { t } = useTranslation("channels");
  const { org, channelUid } = Route.useParams();
  const navigate = useNavigate();

  const { data: channel, isLoading } = useChannel(org, channelUid);
  const update = useUpdateChannel(org, channelUid);
  const remove = useDeleteChannel(org);

  const [form, setForm] = useState<ChannelFormState | null>(null);
  const [deleteOpen, setDeleteOpen] = useState(false);

  if (isLoading || !channel) {
    return (
      <div className="flex items-center justify-center py-12">
        <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
      </div>
    );
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!form) return;
    try {
      await update.mutateAsync({
        name: form.name,
        enabled: form.enabled,
        isDefault: form.isDefault,
        settings: form.settings,
      });
      toast.success(t("saved", "Channel updated"));
    } catch {
      toast.error(t("saveFailed", "Failed to update channel"));
    }
  };

  const handleDelete = async () => {
    try {
      await remove.mutateAsync(channelUid);
      toast.success(t("deleted", "Channel deleted"));
      navigate({ to: "/orgs/$org/channels", params: { org } });
    } catch {
      toast.error(t("deleteFailed", "Delete failed"));
    }
  };

  return (
    <div className="space-y-4 max-w-xl">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight flex items-center gap-2">
            <ChannelIcon type={channel.type} className="h-6 w-6" />
            {channel.name}
          </h1>
          <p className="text-sm text-muted-foreground">
            {channelLabel(channel.type)}
          </p>
        </div>
        <Button asChild variant="ghost" size="sm">
          <Link to="/orgs/$org/channels" params={{ org }}>
            {t("backToList", "Back to channels")}
          </Link>
        </Button>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t("editTitle", "Edit channel")}</CardTitle>
          <CardDescription>
            {t(
              "editSubtitle",
              "Secret values are stored encrypted; updating them replaces the stored value.",
            )}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="space-y-5">
            <ChannelForm type={channel.type} initial={channel} onChange={setForm} />
            <div className="flex justify-end">
              <Button type="submit" disabled={update.isPending || !form?.name}>
                {update.isPending ? (
                  <>
                    <Loader2 className="h-4 w-4 mr-1 animate-spin" />
                    {t("saving", "Saving…")}
                  </>
                ) : (
                  t("save", "Save changes")
                )}
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>

      <Card className="border-destructive/30">
        <CardHeader>
          <CardTitle className="text-destructive text-base">
            {t("dangerZone", "Danger zone")}
          </CardTitle>
        </CardHeader>
        <CardContent>
          <Button
            variant="destructive"
            size="sm"
            onClick={() => setDeleteOpen(true)}
          >
            <Trash2 className="h-4 w-4 mr-1" />
            {t("delete", "Delete channel")}
          </Button>
        </CardContent>
      </Card>

      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("deleteConfirm.title", "Delete this channel?")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(
                "deleteConfirm.body",
                "Bound checks lose this notification target. The action cannot be undone.",
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("cancel", "Cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDelete}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {t("delete", "Delete channel")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
