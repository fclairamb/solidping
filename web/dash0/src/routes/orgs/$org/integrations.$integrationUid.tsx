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
  useIntegration,
  useUpdateIntegration,
  useDeleteIntegration,
} from "@/api/hooks";
import { integrationIconComponent } from "@/components/integrations/integration-icon";
import { PageHeader } from "@/components/shared/page-header";
import {
  IntegrationForm,
  type IntegrationFormState,
} from "@/components/integrations/integration-form";

export const Route = createFileRoute(
  "/orgs/$org/integrations/$integrationUid",
)({
  component: IntegrationDetailPage,
});

function IntegrationDetailPage() {
  const { t } = useTranslation("integrations");
  const { org, integrationUid } = Route.useParams();
  const navigate = useNavigate();

  const { data: integration, isLoading } = useIntegration(org, integrationUid);
  const update = useUpdateIntegration(org, integrationUid);
  const remove = useDeleteIntegration(org);

  const [form, setForm] = useState<IntegrationFormState | null>(null);
  const [deleteOpen, setDeleteOpen] = useState(false);

  if (isLoading || !integration) {
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
      toast.success(t("saved", "Integration updated"));
    } catch {
      toast.error(t("saveFailed", "Failed to update integration"));
    }
  };

  const handleDelete = async () => {
    try {
      await remove.mutateAsync(integrationUid);
      toast.success(t("deleted", "Integration deleted"));
      navigate({ to: "/orgs/$org/integrations", params: { org } });
    } catch {
      toast.error(t("deleteFailed", "Delete failed"));
    }
  };

  const saveLabel = t("save", "Save changes");
  const deleteLabel = t("delete", "Delete integration");
  const backLabel = t("backToList", "Back to integrations");

  return (
    <div className="space-y-6 max-w-2xl">
      <PageHeader
        icon={integrationIconComponent(integration.type)}
        title={integration.name}
        iconClassName="bg-transparent"
        actions={
          <>
            <Button asChild variant="ghost" size="icon" aria-label={backLabel}>
              <Link to="/orgs/$org/integrations" params={{ org }}>
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
          <IntegrationForm
            type={integration.type}
            initial={integration}
            onChange={setForm}
            org={org}
            channelUid={integrationUid}
          />
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
              {t("deleteConfirm.title", "Delete this integration?")}
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
