import { useState } from "react";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { ArrowLeft, Loader2 } from "lucide-react";
import { toast } from "sonner";
import {
  useCheckGroup,
  useUpdateCheckGroup,
  type CheckGroup,
} from "@/api/hooks";
import { ApiError } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Label } from "@/components/ui/label";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { QueryErrorView } from "@/components/shared/error-views";
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

  const waitingForFreshData = !error && !isFetchedAfterMount;

  const goBack = () => navigate({ to: "/orgs/$org/checks", params: { org } });

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
    <CheckGroupEditForm
      org={org}
      group={group}
      isPending={updateGroup.isPending}
      onCancel={goBack}
      onSubmit={async ({ name, description, escalationPolicyUid }) => {
        try {
          await updateGroup.mutateAsync({
            name,
            description,
            escalationPolicyUid,
          });
          toast.success(t("toast.groupUpdated"));
          goBack();
        } catch (err) {
          toast.error(
            err instanceof ApiError
              ? err.message
              : t("toast.groupUpdateFailed"),
          );
        }
      }}
    />
  );
}

interface CheckGroupEditFormSubmit {
  name: string;
  description: string;
  /** "" clears the group's escalation policy, a UID assigns one. */
  escalationPolicyUid: string;
}

function CheckGroupEditForm({
  org,
  group,
  isPending,
  onCancel,
  onSubmit,
}: {
  org: string;
  group: CheckGroup;
  isPending: boolean;
  onCancel: () => void;
  onSubmit: (data: CheckGroupEditFormSubmit) => void;
}) {
  const { t } = useTranslation("checks");
  const [name, setName] = useState(group.name);
  const [description, setDescription] = useState(group.description ?? "");
  const [escalationPolicyUid, setEscalationPolicyUid] = useState(
    group.escalationPolicyUid ?? "",
  );

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    onSubmit({ name: name.trim() || group.name, description, escalationPolicyUid });
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
