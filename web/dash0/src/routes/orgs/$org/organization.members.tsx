import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, redirect } from "@tanstack/react-router";
import { toast } from "sonner";
import { Link } from "@tanstack/react-router";
import { Loader2, Trash2, BellPlus } from "lucide-react";
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
import { CreateInvitationDialog } from "@/components/shared/create-invitation-dialog";
import {
  PagingCoverageCell,
  type MemberCoverageMap,
  buildCoverageMap,
} from "@/components/notifications/member-coverage";
import { useMemberCoverage } from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/organization/members")({
  // Emails sent before the requests URL was fixed (spec
  // 2026-09-05-02) still point here with ?tab=requests — keep them working.
  beforeLoad: ({ params, location }) => {
    if (new URLSearchParams(location.searchStr || "").get("tab") === "requests") {
      throw redirect({
        to: "/orgs/$org/organization/requests",
        params: { org: params.org },
      });
    }
  },
  component: MembersPage,
});

// Owners first, then admins, then the rest — mirrors the backend hierarchy.
const ROLE_ORDER: Record<MemberRole, number> = {
  owner: 0,
  admin: 1,
  user: 2,
  viewer: 3,
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
  // Only an owner may grant or revoke ownership, or touch an owner's row. The
  // server enforces this too (members service, ErrOwnerRoleRequired); hiding
  // the controls just stops the UI from offering a call that would 403.
  const isOwner = user?.isOwner ?? false;
  // Add/re-role/remove are admin-only server-side (RequireOrgAdmin — spec
  // 2026-08-09-03). A viewer or plain user still sees the list (reads stay
  // open) but every row control renders disabled, mirroring the owner-row
  // treatment below.
  const isAdmin = user?.isAdmin ?? false;
  const { data, isLoading, error } = useMembers(org);
  // Paging coverage is admin-only server-side; a non-admin simply gets no
  // coverage column rather than a failed request they cannot act on.
  const { data: coverageData } = useMemberCoverage(org, isAdmin);
  const coverageByUser: MemberCoverageMap = useMemo(
    () => buildCoverageMap(coverageData?.data),
    [coverageData],
  );
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

  // An owner row is read-only for anyone who is not themselves an owner.
  const isLocked = (member: MemberResponse) =>
    member.role === "owner" && !isOwner;

  // Reasons a row's controls render disabled, in priority order (each has its
  // own tooltip copy). Non-admin is checked before the owner lock so a viewer
  // sees "admin required" rather than the more specific owner-only message.
  type LockReason = "self" | "admin" | "owner" | null;
  const lockReason = (member: MemberResponse): LockReason => {
    if (isSelf(member)) return "self";
    if (!isAdmin) return "admin";
    if (isLocked(member)) return "owner";
    return null;
  };

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

  const lockTooltip = (reason: NonNullable<LockReason>) => {
    switch (reason) {
      case "self":
        return t("members.cannotEditSelf");
      case "admin":
        return t("members.adminOnly");
      case "owner":
        return t("members.ownerOnly");
    }
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
      <div className="flex items-center justify-end mb-6">
        <CreateInvitationDialog org={org} />
      </div>
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
                  <TableHead className="hidden md:table-cell">
                    {t("members.column.email")}
                  </TableHead>
                  <TableHead>{t("members.column.role")}</TableHead>
                  {isAdmin && (
                    <TableHead className="hidden lg:table-cell">
                      {t("members.column.coverage")}
                    </TableHead>
                  )}
                  <TableHead className="hidden lg:table-cell">
                    {t("members.column.joinedAt")}
                  </TableHead>
                  <TableHead className="w-[120px]" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {sortedMembers.map((member) => {
                  const reason = lockReason(member);
                  const joined = member.joinedAt || member.createdAt;
                  return (
                    <TableRow key={member.uid}>
                      <TableCell className="max-w-0">
                        <div className="flex min-w-0 items-center gap-3">
                          <span className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-muted text-xs font-medium">
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
                          <span className="min-w-0 truncate font-medium">
                            {member.name || member.email}
                          </span>
                        </div>
                      </TableCell>
                      <TableCell className="hidden text-muted-foreground md:table-cell">
                        {member.email}
                      </TableCell>
                      <TableCell>
                        {reason ? (
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <span className="inline-block">
                                <Select value={member.role} disabled>
                                  <SelectTrigger
                                    className="w-[140px]"
                                    data-testid={`member-role-${member.email}`}
                                  >
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
                            <TooltipContent>{lockTooltip(reason)}</TooltipContent>
                          </Tooltip>
                        ) : (
                          <Select
                            value={member.role}
                            onValueChange={(value) =>
                              handleRoleChange(member, value as MemberRole)
                            }
                            disabled={updateMember.isPending}
                          >
                            <SelectTrigger
                              className="w-[140px]"
                              data-testid={`member-role-${member.email}`}
                            >
                              <SelectValue />
                            </SelectTrigger>
                            <SelectContent>
                              {isOwner && (
                                <SelectItem value="owner">
                                  {t("members.role.owner")}
                                </SelectItem>
                              )}
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
                      {isAdmin && (
                        <TableCell className="hidden lg:table-cell">
                          <PagingCoverageCell
                            coverage={coverageByUser.get(member.userUid)}
                          />
                        </TableCell>
                      )}
                      <TableCell className="hidden text-muted-foreground lg:table-cell">
                        {new Date(joined).toLocaleDateString()}
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center justify-end gap-1">
                        {isAdmin && (
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Button
                                variant="ghost"
                                size="icon"
                                asChild
                                aria-label={t("members.setUpPaging")}
                                data-testid={`member-paging-${member.email}`}
                              >
                                <Link
                                  to="/orgs/$org/organization/members/$memberUid/paging"
                                  params={{ org, memberUid: member.uid }}
                                >
                                  <BellPlus className="h-4 w-4" />
                                </Link>
                              </Button>
                            </TooltipTrigger>
                            <TooltipContent>
                              {t("members.setUpPaging")}
                            </TooltipContent>
                          </Tooltip>
                        )}
                        {reason ? (
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
                            <TooltipContent>{lockTooltip(reason)}</TooltipContent>
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
                        </div>
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
