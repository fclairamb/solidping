import { useState } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { ArrowLeft, Loader2, Trash2 } from "lucide-react";
import { toast } from "sonner";
import {
  useCheckGroup,
  useDeleteCheckGroup,
  useUpdateCheckGroup,
  type CheckGroup,
} from "@/api/hooks";
import { ApiError, getApiErrorField } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Label } from "@/components/ui/label";
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
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorView } from "@/components/shared/error-views";
import { DocsLink } from "@/components/shared/docs-link";
import { EscalationSelect } from "@/components/checks/form/sections/escalation";

export const Route = createFileRoute("/orgs/$org/check-groups/$uid/edit")({
  component: CheckGroupEditPage,
});

function CheckGroupEditPage() {
  const { t } = useTranslation("checks");
  const navigate = useNavigate();
  const { org, uid } = Route.useParams();

  // Mirrors checks.$checkUid.edit.tsx: force a fresh fetch on mount and hold
  // the skeleton until it lands (isFetchedAfterMount), so CheckGroupEditForm
  // below only ever mounts (and seeds its local state via useState) from
  // fresh data, never a stale cache entry.
  const {
    data: group,
    isLoading,
    isFetchedAfterMount,
    error,
    refetch,
  } = useCheckGroup(org, uid, { refetchOnMount: "always" });
  const updateGroup = useUpdateCheckGroup(org, uid);
  const deleteGroup = useDeleteCheckGroup(org);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const waitingForFreshData = !error && !isFetchedAfterMount;

  const goBack = () => navigate({ to: "/orgs/$org/checks", params: { org } });

  const handleDelete = async () => {
    try {
      await deleteGroup.mutateAsync(uid);
      toast.success(t("toast.groupDeleted"));
      goBack();
    } catch (err) {
      toast.error(
        err instanceof ApiError ? err.message : t("toast.groupDeleteFailed"),
      );
    }
  };

  if (isLoading || waitingForFreshData) {
    return (
      <div className="space-y-6 max-w-2xl">
        <div className="flex items-center gap-4">
          <Skeleton className="h-10 w-10 rounded" />
          <Skeleton className="h-8 w-48" />
        </div>
        <Skeleton className="h-96 rounded-lg" />
      </div>
    );
  }

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource={t("dialog.group")}
        backTo="/orgs/$org/checks"
        backLabel={t("form.editCheck")}
        onRetry={() => refetch()}
      />
    );
  }

  if (!group) return null;

  return (
    <>
      <CheckGroupEditForm
        org={org}
        group={group}
        isPending={updateGroup.isPending}
        onCancel={goBack}
        onDelete={() => setConfirmDelete(true)}
        onSubmit={async ({ name, description, escalationPolicyUid, slug }) => {
          await updateGroup.mutateAsync({
            name,
            description,
            escalationPolicyUid,
            slug,
          });
          toast.success(t("toast.groupUpdated"));
          goBack();
        }}
      />

      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("dialog.deleteGroupTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("dialog.deleteGroupDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("groupForm.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDelete}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              data-testid="confirm-delete-group"
            >
              {t("menu.deleteGroup")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

interface CheckGroupEditFormSubmit {
  name: string;
  description: string;
  /** "" clears the group's escalation policy, a UID assigns one. */
  escalationPolicyUid: string;
  slug: string;
}

// Group slugs are 3-100 chars: a lowercase letter followed by 2-99 lowercase
// letters/digits/hyphens. This mirrors slugRegex in
// server/internal/handlers/checkgroups/service.go — same 3-100 rule as the
// check form's slugRegex (check-form.tsx), which validates a different
// resource.
const groupSlugRegex = /^[a-z][a-z0-9-]{2,99}$/;

function CheckGroupEditForm({
  org,
  group,
  isPending,
  onCancel,
  onDelete,
  onSubmit,
}: {
  org: string;
  group: CheckGroup;
  isPending: boolean;
  onCancel: () => void;
  onDelete: () => void;
  onSubmit: (data: CheckGroupEditFormSubmit) => Promise<void>;
}) {
  const { t } = useTranslation("checks");
  const [name, setName] = useState(group.name);
  const [description, setDescription] = useState(group.description ?? "");
  const [escalationPolicyUid, setEscalationPolicyUid] = useState(
    group.escalationPolicyUid ?? "",
  );
  // Editing an *existing* group: the slug is already user-owned, so renaming
  // the group must never silently rewrite it. Unlike the create dialog, this
  // field never auto-derives from `name` edits.
  const [slug, setSlug] = useState(group.slug);
  const [slugServerError, setSlugServerError] = useState<string | undefined>(
    undefined,
  );

  const slugClientError = !groupSlugRegex.test(slug)
    ? t("groupForm.slugInvalid")
    : undefined;
  const slugError = slugClientError ?? slugServerError;

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (slugClientError) return;
    setSlugServerError(undefined);
    try {
      await onSubmit({
        name: name.trim() || group.name,
        description,
        escalationPolicyUid,
        slug,
      });
    } catch (err) {
      const fieldMessage = getApiErrorField(err, "slug");
      if (fieldMessage && err instanceof ApiError) {
        // Both a duplicate slug and an invalid format come back as a
        // VALIDATION_ERROR with a "slug" field entry (handler.go); the
        // top-level title is the only thing that tells them apart, so use it
        // to pick the localized message shown inline on the field.
        setSlugServerError(
          err.message.toLowerCase().includes("already exists")
            ? t("groupForm.slugTaken")
            : t("groupForm.slugInvalid"),
        );
      } else {
        toast.error(
          err instanceof ApiError ? err.message : t("toast.groupUpdateFailed"),
        );
      }
    }
  };

  return (
    <form onSubmit={handleSubmit} className="space-y-6 max-w-2xl">
      <div className="flex items-center gap-4">
        <Button
          type="button"
          variant="ghost"
          size="icon"
          onClick={onCancel}
          aria-label={t("groupForm.cancel")}
        >
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <h1 className="text-2xl sm:text-3xl font-bold tracking-tight">
          {t("groupForm.title")}
        </h1>
        <div className="ml-auto flex items-center gap-2">
          <Button
            type="button"
            variant="destructive"
            onClick={onDelete}
            aria-label={t("menu.deleteGroup")}
            data-testid="group-delete-button"
          >
            <Trash2 className="sm:mr-2 h-4 w-4" />
            <span className="hidden sm:inline">{t("menu.deleteGroup")}</span>
          </Button>
          <DocsLink href="/docs/features/check-groups" />
        </div>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t("groupForm.detailsTitle")}</CardTitle>
          <CardDescription>{t("groupForm.detailsDescription")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="group-edit-name">{t("dialog.groupName")}</Label>
            <Input
              id="group-edit-name"
              data-testid="group-edit-name-input"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t("dialog.groupNamePlaceholder")}
              required
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="group-edit-slug">{t("groupForm.slug")}</Label>
            <Input
              id="group-edit-slug"
              data-testid="group-edit-slug-input"
              value={slug}
              onChange={(e) => {
                setSlug(e.target.value);
                setSlugServerError(undefined);
              }}
              placeholder="prod-eu-west"
              className={slugError ? "border-destructive" : ""}
              aria-invalid={!!slugError}
              required
            />
            {slugError ? (
              <p
                className="text-xs text-destructive"
                data-testid="group-edit-slug-error"
              >
                {slugError}
              </p>
            ) : (
              <p className="text-xs text-muted-foreground">
                {t("groupForm.slugHelp")}
              </p>
            )}
          </div>

          <div className="space-y-2">
            <Label htmlFor="group-edit-description">
              {t("groupForm.description")}
            </Label>
            <Textarea
              id="group-edit-description"
              data-testid="group-edit-description-input"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder={t("groupForm.descriptionPlaceholder")}
              rows={3}
            />
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("groupForm.escalationTitle")}</CardTitle>
          <CardDescription>
            {t("groupForm.escalationDescription")}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <EscalationSelect
            org={org}
            variant="group"
            value={escalationPolicyUid}
            onChange={setEscalationPolicyUid}
          />
        </CardContent>
      </Card>

      <div className="flex justify-end gap-2">
        <Button type="button" variant="outline" onClick={onCancel}>
          {t("groupForm.cancel")}
        </Button>
        <Button
          type="submit"
          disabled={isPending}
          data-testid="group-edit-submit"
        >
          {isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
          {t("groupForm.save")}
        </Button>
      </div>
    </form>
  );
}
