import { useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, useNavigate, Link } from "@tanstack/react-router";
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
import { ArrowLeft, Loader2, Save, Trash2 } from "lucide-react";
import { toast } from "sonner";
import {
  useChannel,
  useUpdateChannel,
  useDeleteChannel,
} from "@/api/hooks";
import { channelIconComponent } from "@/components/channels/channel-icon";
import { PageHeader } from "@/components/shared/page-header";
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

  const saveLabel = t("save", "Save changes");
  const deleteLabel = t("delete", "Delete channel");
  const backLabel = t("backToList", "Back to channels");

  return (
    <div className="space-y-6 max-w-2xl">
      <PageHeader
        icon={channelIconComponent(channel.type)}
        title={channel.name}
        iconClassName="bg-transparent"
        actions={
          <>
            <Button asChild variant="ghost" size="icon" aria-label={backLabel}>
              <Link to="/orgs/$org/channels" params={{ org }}>
                <ArrowLeft />
              </Link>
            </Button>
            <Button
              variant="destructive"
              onClick={() => setDeleteOpen(true)}
              aria-label={deleteLabel}
            >
              <Trash2 />
              <span className="hidden sm:inline">{deleteLabel}</span>
            </Button>
          </>
        }
      />

      <div className="rounded-md border bg-card p-4">
        <p className="mb-4 text-sm text-muted-foreground">
          {t(
            "editSubtitle",
            "Secret values are stored encrypted; updating them replaces the stored value.",
          )}
        </p>
        <form onSubmit={handleSubmit} className="space-y-4">
          <ChannelForm type={channel.type} initial={channel} onChange={setForm} />
          <div className="flex justify-end">
            <Button
              type="submit"
              disabled={update.isPending || !form?.name}
              aria-label={saveLabel}
            >
              {update.isPending ? (
                <Loader2 className="animate-spin" />
              ) : (
                <Save />
              )}
              <span className="hidden sm:inline">
                {update.isPending ? t("saving", "Saving…") : saveLabel}
              </span>
            </Button>
          </div>
        </form>
      </div>

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
              {deleteLabel}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
