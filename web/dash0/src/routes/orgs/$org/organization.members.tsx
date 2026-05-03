import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute } from "@tanstack/react-router";
import { toast } from "sonner";
import { Loader2, Trash2 } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
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
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { ApiError } from "@/api/client";
import {
  type MemberResponse,
  type MemberRole,
  useMembers,
  useUpdateMember,
  useRemoveMember,
} from "@/api/hooks";
import { useAuth } from "@/contexts/AuthContext";

export const Route = createFileRoute("/orgs/$org/organization/members")({
  component: MembersPage,
});

const ROLE_ORDER: Record<MemberRole, number> = {
  admin: 0,
  user: 1,
  viewer: 2,
};

function initialsFor(member: MemberResponse): string {
  const source = member.name?.trim() || member.email;
  const parts = source.split(/\s+/).filter(Boolean);
  if (parts.length >= 2) {
    return (parts[0][0] + parts[1][0]).toUpperCase();
  }
  return source.slice(0, 2).toUpperCase();
}

function MembersPage() {
  const { t } = useTranslation("org");
  const { t: tc } = useTranslation("common");
  const { org } = Route.useParams();
  const { user } = useAuth();
  const { data, isLoading, error } = useMembers(org);
  const updateMember = useUpdateMember(org);
  const removeMember = useRemoveMember(org);

  const [removeTarget, setRemoveTarget] = useState<MemberResponse | null>(null);
  const [demoteTarget, setDemoteTarget] = useState<MemberResponse | null>(null);

  const sortedMembers = useMemo(() => {
    const members = data?.data ?? [];
    return [...members].sort((a, b) => {
      const roleDiff = ROLE_ORDER[a.role] - ROLE_ORDER[b.role];
      if (roleDiff !== 0) return roleDiff;
      const aLabel = (a.name || a.email).toLowerCase();
      const bLabel = (b.name || b.email).toLowerCase();
      return aLabel.localeCompare(bLabel);
    });
  }, [data]);

  const isSelf = (member: MemberResponse) => member.email === user?.email;

  const applyRoleChange = async (member: MemberResponse, role: MemberRole) => {
    try {
      await updateMember.mutateAsync({ uid: member.uid, role });
      toast.success(t("members.roleUpdated"));
    } catch (err) {
      const message = err instanceof ApiError ? err.message : t("members.updateFailed");
      toast.error(message);
    }
  };

  const handleRoleChange = (member: MemberResponse, role: MemberRole) => {
    if (role === member.role) return;
    if (role === "viewer" && member.role !== "viewer") {
      setDemoteTarget(member);
      return;
    }
    void applyRoleChange(member, role);
  };

  const confirmDemote = async () => {
    if (!demoteTarget) return;
    const target = demoteTarget;
    setDemoteTarget(null);
    await applyRoleChange(target, "viewer");
  };

  const confirmRemove = async () => {
    if (!removeTarget) return;
    const target = removeTarget;
    setRemoveTarget(null);
    try {
      await removeMember.mutateAsync(target.uid);
      toast.success(t("members.memberRemoved"));
    } catch (err) {
      const message = err instanceof ApiError ? err.message : t("members.removeFailed");
      toast.error(message);
    }
  };

  return (
    <TooltipProvider>
      <Card>
        <CardHeader>
          <CardTitle>{t("members.title")}</CardTitle>
          <CardDescription>{t("members.subtitle")}</CardDescription>
        </CardHeader>
        <CardContent>
          {isLoading ? (
            <div className="flex items-center justify-center py-8">
              <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
            </div>
          ) : error ? (
            <p className="text-sm text-destructive py-4">
              {error instanceof Error ? error.message : tc("unexpectedError")}
            </p>
          ) : sortedMembers.length === 0 ? (
            <p className="text-center text-muted-foreground py-8">
              {t("members.empty")}
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("members.column.member")}</TableHead>
                  <TableHead>{t("members.column.email")}</TableHead>
                  <TableHead>{t("members.column.role")}</TableHead>
                  <TableHead>{t("members.column.joinedAt")}</TableHead>
                  <TableHead className="w-[80px]" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {sortedMembers.map((member) => {
                  const self = isSelf(member);
                  const joined = member.joinedAt || member.createdAt;
                  return (
                    <TableRow key={member.uid}>
                      <TableCell>
                        <div className="flex items-center gap-3">
                          <span className="inline-flex h-8 w-8 items-center justify-center rounded-full bg-muted text-xs font-medium">
                            {member.avatarUrl ? (
                              <img
                                src={member.avatarUrl}
                                alt=""
                                className="h-8 w-8 rounded-full object-cover"
                              />
                            ) : (
                              initialsFor(member)
                            )}
                          </span>
                          <span className="font-medium">
                            {member.name || member.email}
                          </span>
                        </div>
                      </TableCell>
                      <TableCell className="text-muted-foreground">
                        {member.email}
                      </TableCell>
                      <TableCell>
                        {self ? (
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <span className="inline-block">
                                <Select value={member.role} disabled>
                                  <SelectTrigger className="w-[140px]">
                                    <SelectValue />
                                  </SelectTrigger>
                                  <SelectContent>
                                    <SelectItem value={member.role}>
                                      {t(`members.role.${member.role}`)}
                                    </SelectItem>
                                  </SelectContent>
                                </Select>
                              </span>
                            </TooltipTrigger>
                            <TooltipContent>
                              {t("members.cannotEditSelf")}
                            </TooltipContent>
                          </Tooltip>
                        ) : (
                          <Select
                            value={member.role}
                            onValueChange={(value) =>
                              handleRoleChange(member, value as MemberRole)
                            }
                            disabled={updateMember.isPending}
                          >
                            <SelectTrigger className="w-[140px]">
                              <SelectValue />
                            </SelectTrigger>
                            <SelectContent>
                              <SelectItem value="admin">
                                {t("members.role.admin")}
                              </SelectItem>
                              <SelectItem value="user">
                                {t("members.role.user")}
                              </SelectItem>
                              <SelectItem value="viewer">
                                {t("members.role.viewer")}
                              </SelectItem>
                            </SelectContent>
                          </Select>
                        )}
                      </TableCell>
                      <TableCell className="text-muted-foreground">
                        {new Date(joined).toLocaleDateString()}
                      </TableCell>
                      <TableCell>
                        {self ? (
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <span className="inline-block">
                                <Button
                                  variant="ghost"
                                  size="icon"
                                  disabled
                                  aria-label={t("members.remove")}
                                >
                                  <Trash2 className="h-4 w-4 text-muted-foreground" />
                                </Button>
                              </span>
                            </TooltipTrigger>
                            <TooltipContent>
                              {t("members.cannotEditSelf")}
                            </TooltipContent>
                          </Tooltip>
                        ) : (
                          <Button
                            variant="ghost"
                            size="icon"
                            onClick={() => setRemoveTarget(member)}
                            disabled={removeMember.isPending}
                            aria-label={t("members.remove")}
                          >
                            <Trash2 className="h-4 w-4 text-destructive" />
                          </Button>
                        )}
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <AlertDialog
        open={removeTarget !== null}
        onOpenChange={(open) => !open && setRemoveTarget(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("members.removeConfirm.title")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("members.removeConfirm.body", {
                email: removeTarget?.email ?? "",
                org,
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tc("cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={confirmRemove}>
              {t("members.removeConfirm.action")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={demoteTarget !== null}
        onOpenChange={(open) => !open && setDemoteTarget(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("members.demoteConfirm.title")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("members.demoteConfirm.body", {
                email: demoteTarget?.email ?? "",
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{tc("cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={confirmDemote}>
              {t("members.demoteConfirm.action")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </TooltipProvider>
  );
}
