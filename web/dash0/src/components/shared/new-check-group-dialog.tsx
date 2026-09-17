import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import { Loader2 } from "lucide-react";

import { ApiError, getApiErrorField } from "@/api/client";
import { useCreateCheckGroup, type CheckGroup } from "@/api/hooks";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { slugify } from "@/lib/utils";

// Same rule the checks list dialog enforces before it posts, so an obviously
// bad slug is caught here rather than coming back as a 400.
const groupSlugRegex = /^[a-z][a-z0-9-]{2,99}$/;

export interface NewCheckGroupDialogProps {
  org: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Called with the created group, so a caller can select it immediately. */
  onCreated?: (group: CheckGroup) => void;
}

/**
 * Create-a-group dialog, extracted so the check form can offer it inline.
 *
 * Until spec 2026-09-16-13 the ONLY way to create a check group was the button
 * on the checks list, and the check form hid its group field entirely while an
 * organization had none — so a new user could never meet the feature. The form
 * now always renders the field, and this dialog is what "create one" behind it
 * opens.
 */
export function NewCheckGroupDialog({
  org,
  open,
  onOpenChange,
  onCreated,
}: NewCheckGroupDialogProps) {
  const { t } = useTranslation("checks");
  const { t: tc } = useTranslation("common");
  const createGroup = useCreateCheckGroup(org);

  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugEdited, setSlugEdited] = useState(false);
  const [slugError, setSlugError] = useState<string | undefined>();

  const close = () => {
    onOpenChange(false);
    setName("");
    setSlug("");
    setSlugEdited(false);
    setSlugError(undefined);
  };

  const submit = async () => {
    if (!name.trim()) return;

    const trimmedSlug = slug.trim();
    if (trimmedSlug && !groupSlugRegex.test(trimmedSlug)) {
      setSlugError(t("dialog.groupSlugInvalid"));

      return;
    }

    try {
      const created = await createGroup.mutateAsync({
        name: name.trim(),
        // Untouched and empty sends nothing, so the server derives the slug
        // from the name — same contract as the checks list dialog.
        ...(trimmedSlug ? { slug: trimmedSlug } : {}),
      });
      toast.success(t("toast.groupCreated"));
      onCreated?.(created);
      close();
    } catch (err) {
      const fieldMessage = getApiErrorField(err, "slug");
      if (fieldMessage && err instanceof ApiError) {
        setSlugError(
          err.message.toLowerCase().includes("already exists")
            ? t("dialog.groupSlugTaken")
            : t("dialog.groupSlugInvalid"),
        );
      } else {
        toast.error(err instanceof ApiError ? err.message : t("toast.groupCreateFailed"));
      }
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (next) {
          onOpenChange(true);
        } else {
          close();
        }
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("dialog.newGroupTitle")}</DialogTitle>
        </DialogHeader>
        <div className="space-y-4 py-2">
          <div className="space-y-2">
            <Label htmlFor="inline-group-name">{tc("name")}</Label>
            <Input
              id="inline-group-name"
              placeholder={t("dialog.groupNamePlaceholder")}
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                if (!slugEdited) setSlug(slugify(e.target.value));
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter") void submit();
              }}
              autoFocus
              data-testid="inline-new-group-name-input"
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="inline-group-slug">{t("dialog.groupSlug")}</Label>
            <Input
              id="inline-group-slug"
              placeholder="prod-eu-west"
              value={slug}
              onChange={(e) => {
                setSlug(e.target.value);
                setSlugEdited(true);
                setSlugError(undefined);
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter") void submit();
              }}
              className={slugError ? "border-destructive" : ""}
              aria-invalid={!!slugError}
              data-testid="inline-new-group-slug-input"
            />
            {slugError ? (
              <p className="text-xs text-destructive" data-testid="inline-new-group-slug-error">
                {slugError}
              </p>
            ) : (
              <p className="text-xs text-muted-foreground">{t("dialog.groupSlugHelp")}</p>
            )}
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={close}>
            {tc("cancel")}
          </Button>
          <Button
            onClick={() => void submit()}
            disabled={!name.trim() || createGroup.isPending}
            data-testid="inline-new-group-submit"
          >
            {createGroup.isPending ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : null}
            {tc("create")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
