import { useState } from "react";
import { createFileRoute } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { KeyRound, Plus, RotateCw, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { ApiError } from "@/api/client";
import {
  useDeleteOrgParameter,
  useOrgParameters,
  useSetOrgParameter,
  type OrgParameter,
} from "@/api/hooks";
import { PageHeader } from "@/components/shared/page-header";
import { QueryErrorView } from "@/components/shared/error-views";
import { CopyableCode } from "@/components/shared/copyable-code";
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
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { PasswordInput } from "@/components/ui/password-input";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { TimeAgo } from "@/components/ui/time-ago";

export const Route = createFileRoute("/orgs/$org/organization/parameters")({
  component: OrgParametersPage,
});

/** The key shape the API enforces — mirrored here only to fail fast in the form. */
const KEY_PATTERN = /^[a-z][a-z0-9_.-]{0,63}$/;

function OrgParametersPage() {
  const { t } = useTranslation(["org", "common"]);
  const { org } = Route.useParams();
  const { data: parameters, isLoading, error, refetch } = useOrgParameters(org);
  const setParameter = useSetOrgParameter(org);
  const deleteParameter = useDeleteOrgParameter(org);

  // `editing` is the parameter being rotated (key fixed), or "new" for a
  // creation, or null for a closed dialog.
  const [editing, setEditing] = useState<OrgParameter | "new" | null>(null);
  const [pendingDelete, setPendingDelete] = useState<OrgParameter | null>(null);
  const [keyInput, setKeyInput] = useState("");
  const [valueInput, setValueInput] = useState("");
  const [secretInput, setSecretInput] = useState(true);

  const isRotation = editing !== null && editing !== "new";
  const keyIsValid = isRotation || KEY_PATTERN.test(keyInput);

  const openCreate = () => {
    setKeyInput("");
    setValueInput("");
    setSecretInput(true);
    setEditing("new");
  };

  const openRotate = (parameter: OrgParameter) => {
    setKeyInput(parameter.key);
    // Never prefilled, even for a non-secret one: this form only ever WRITES a
    // value, and a prefilled field invites "I only changed the flag" edits that
    // silently rewrite the value.
    setValueInput("");
    setSecretInput(parameter.secret);
    setEditing(parameter);
  };

  const submit = async () => {
    const key = isRotation ? editing.key : keyInput.trim();
    if (!key || !valueInput) return;

    try {
      await setParameter.mutateAsync({ key, value: valueInput, secret: secretInput });
      toast.success(
        isRotation ? t("parameters.rotated", { key }) : t("parameters.created", { key }),
      );
      setEditing(null);
      setValueInput("");
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : t("parameters.saveFailed"));
    }
  };

  return (
    <div className="space-y-6">
      <PageHeader
        icon={KeyRound}
        title={t("parameters.title")}
        description={t("parameters.subtitle")}
        docsHref="/docs/cli#secrets-parameters-and-references"
        actions={
          <Button onClick={openCreate} data-testid="parameter-add">
            <Plus className="mr-1.5 h-4 w-4" />
            {t("parameters.add")}
          </Button>
        }
      />

      {isLoading ? (
        <div className="space-y-2">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      ) : error ? (
        <QueryErrorView error={error} org={org} onRetry={() => void refetch()} />
      ) : !parameters || parameters.length === 0 ? (
        <div
          className="rounded-lg border border-dashed p-8 text-center"
          data-testid="parameters-empty"
        >
          <KeyRound className="mx-auto mb-3 h-8 w-8 text-muted-foreground" />
          <p className="font-medium">{t("parameters.empty.title")}</p>
          <p className="mt-1 text-sm text-muted-foreground">
            {t("parameters.empty.description")}
          </p>
        </div>
      ) : (
        <div className="overflow-x-auto rounded-lg border">
          <Table data-testid="parameters-table">
            <TableHeader>
              <TableRow>
                <TableHead>{t("parameters.columns.key")}</TableHead>
                <TableHead>{t("parameters.columns.value")}</TableHead>
                <TableHead>{t("parameters.columns.reference")}</TableHead>
                <TableHead>{t("parameters.columns.updated")}</TableHead>
                <TableHead className="text-right">{t("common:actions")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {parameters.map((parameter) => (
                <TableRow key={parameter.key} data-testid={`parameter-row-${parameter.key}`}>
                  <TableCell className="font-mono text-sm">{parameter.key}</TableCell>
                  <TableCell>
                    {parameter.secret ? (
                      // Not a masked value: the API sends no value at all for a
                      // secret parameter, and the badge says so rather than
                      // showing dots that imply one could be revealed.
                      <Badge variant="secondary" data-testid="parameter-write-only">
                        {t("parameters.writeOnly")}
                      </Badge>
                    ) : (
                      <span className="font-mono text-sm">{parameter.value}</span>
                    )}
                  </TableCell>
                  <TableCell>
                    <CopyableCode code={`\${param:${parameter.key}}`} data-testid="parameter-reference" />
                  </TableCell>
                  <TableCell>
                    <TimeAgo date={parameter.updatedAt} />
                  </TableCell>
                  <TableCell className="text-right">
                    <div className="flex justify-end gap-1">
                      <Button
                        variant="ghost"
                        size="icon"
                        aria-label={t("parameters.rotate")}
                        data-testid="parameter-row-rotate"
                        onClick={() => openRotate(parameter)}
                      >
                        <RotateCw className="h-4 w-4" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="text-destructive hover:text-destructive"
                        aria-label={t("common:delete")}
                        data-testid="parameter-row-delete"
                        onClick={() => setPendingDelete(parameter)}
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      <Dialog open={editing !== null} onOpenChange={(open) => !open && setEditing(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {isRotation ? t("parameters.rotateTitle") : t("parameters.addTitle")}
            </DialogTitle>
            <DialogDescription>
              {isRotation
                ? t("parameters.rotateDescription")
                : t("parameters.addDescription")}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="parameter-key">{t("parameters.columns.key")}</Label>
              <Input
                id="parameter-key"
                data-testid="parameter-key-input"
                value={keyInput}
                disabled={isRotation}
                placeholder="sso-authtest-password"
                onChange={(event) => setKeyInput(event.target.value)}
              />
              {!isRotation && keyInput !== "" && !keyIsValid && (
                <p className="text-sm text-destructive" data-testid="parameter-key-error">
                  {t("parameters.keyInvalid")}
                </p>
              )}
            </div>

            <div className="space-y-2">
              <Label htmlFor="parameter-value">{t("parameters.columns.value")}</Label>
              <PasswordInput
                id="parameter-value"
                data-testid="parameter-value-input"
                value={valueInput}
                onChange={(event) => setValueInput(event.target.value)}
              />
            </div>

            <div className="flex items-center justify-between gap-4 rounded-lg border p-3">
              <div>
                <Label htmlFor="parameter-secret">{t("parameters.secretLabel")}</Label>
                <p className="text-sm text-muted-foreground">
                  {t("parameters.secretHelp")}
                </p>
              </div>
              <Switch
                id="parameter-secret"
                data-testid="parameter-secret-switch"
                checked={secretInput}
                onCheckedChange={setSecretInput}
              />
            </div>
          </div>

          <DialogFooter>
            <Button variant="outline" onClick={() => setEditing(null)}>
              {t("common:cancel")}
            </Button>
            <Button
              data-testid="parameter-save"
              disabled={!keyIsValid || valueInput === "" || setParameter.isPending}
              onClick={() => void submit()}
            >
              {t("common:save")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <AlertDialog
        open={!!pendingDelete}
        onOpenChange={(open) => !open && setPendingDelete(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("parameters.delete.title")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("parameters.delete.description", { key: pendingDelete?.key ?? "" })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common:cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              data-testid="parameter-delete-confirm"
              onClick={async () => {
                if (!pendingDelete) return;
                try {
                  await deleteParameter.mutateAsync(pendingDelete.key);
                  toast.success(t("parameters.delete.deleted"));
                } catch (err) {
                  toast.error(
                    err instanceof ApiError ? err.message : t("parameters.delete.failed"),
                  );
                } finally {
                  setPendingDelete(null);
                }
              }}
            >
              <Trash2 className="mr-1.5 h-4 w-4" />
              {t("common:delete")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
