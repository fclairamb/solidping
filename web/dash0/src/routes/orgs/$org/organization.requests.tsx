import { useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute } from "@tanstack/react-router";
import { Button } from "@/components/ui/button";
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
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Loader2, Check, X } from "lucide-react";
import { toast } from "sonner";
import {
  useOrgMembershipRequests,
  useApproveMembershipRequest,
  useRejectMembershipRequest,
  type MembershipRequestAdminView,
  type MembershipRequestStatus,
} from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/organization/requests")({
  component: RequestsPage,
});

function RequestsPage() {
  const { t } = useTranslation("org");
  const { t: tc } = useTranslation("common");
  const { org } = Route.useParams();

  const [tab, setTab] = useState<"pending" | "rejected" | "approved" | "all">(
    "pending",
  );

  const status: MembershipRequestStatus | undefined =
    tab === "all" ? undefined : tab;

  const { data, isLoading } = useOrgMembershipRequests(org, { status });
  const requests = data?.data || [];

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle>{t("requests.title", "Membership requests")}</CardTitle>
          <CardDescription>
            {t(
              "requests.description",
              "Approve or reject users asking to join.",
            )}
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="mb-4 flex gap-2">
            {(["pending", "rejected", "approved", "all"] as const).map((s) => (
              <Button
                key={s}
                variant={tab === s ? "default" : "outline"}
                size="sm"
                onClick={() => setTab(s)}
              >
                {t(`requests.${s}`, s.charAt(0).toUpperCase() + s.slice(1))}
              </Button>
            ))}
          </div>

          {isLoading ? (
            <div className="flex items-center justify-center py-8">
              <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
            </div>
          ) : requests.length === 0 ? (
            <p className="text-center text-muted-foreground py-8">
              {t("requests.empty", "No requests in this view.")}
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{tc("email")}</TableHead>
                  <TableHead>{t("requests.message", "Message")}</TableHead>
                  <TableHead>{t("requests.submittedAt", "Submitted")}</TableHead>
                  <TableHead>{t("requests.status", "Status")}</TableHead>
                  <TableHead className="w-[160px]" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {requests.map((req) => (
                  <RequestRow key={req.uid} org={org} req={req} />
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function RequestRow({
  org,
  req,
}: {
  org: string;
  req: MembershipRequestAdminView;
}) {
  const { t } = useTranslation("org");
  const [approveOpen, setApproveOpen] = useState(false);
  const [rejectOpen, setRejectOpen] = useState(false);

  return (
    <>
      <TableRow>
        <TableCell>
          <div>
            <div className="font-medium">{req.user.name || req.user.email}</div>
            {req.user.name && (
              <div className="text-xs text-muted-foreground">
                {req.user.email}
              </div>
            )}
          </div>
        </TableCell>
        <TableCell className="max-w-xs truncate text-sm text-muted-foreground">
          {req.message || "—"}
        </TableCell>
        <TableCell className="text-sm text-muted-foreground">
          {new Date(req.createdAt).toLocaleString()}
        </TableCell>
        <TableCell className="text-sm capitalize">{req.status}</TableCell>
        <TableCell>
          {req.status === "pending" && (
            <div className="flex gap-2">
              <Button
                variant="outline"
                size="sm"
                onClick={() => setApproveOpen(true)}
              >
                <Check className="h-4 w-4 mr-1" />
                {t("requests.approve", "Approve")}
              </Button>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => setRejectOpen(true)}
              >
                <X className="h-4 w-4 mr-1" />
                {t("requests.reject", "Reject")}
              </Button>
            </div>
          )}
          {req.status === "rejected" && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => setApproveOpen(true)}
            >
              {t("requests.approveAnyway", "Approve")}
            </Button>
          )}
        </TableCell>
      </TableRow>

      <ApproveDialog
        org={org}
        uid={req.uid}
        open={approveOpen}
        onOpenChange={setApproveOpen}
      />
      <RejectDialog
        org={org}
        uid={req.uid}
        open={rejectOpen}
        onOpenChange={setRejectOpen}
      />
    </>
  );
}

function ApproveDialog({
  org,
  uid,
  open,
  onOpenChange,
}: {
  org: string;
  uid: string;
  open: boolean;
  onOpenChange: (b: boolean) => void;
}) {
  const { t } = useTranslation("org");
  const { t: tc } = useTranslation("common");
  const approve = useApproveMembershipRequest(org);
  const [role, setRole] = useState<string>("user");

  const handleSubmit = async () => {
    try {
      await approve.mutateAsync({ uid, role });
      toast.success(t("requests.approved", "Request approved"));
      onOpenChange(false);
    } catch {
      toast.error(t("requests.approveFailed", "Failed to approve request"));
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t("requests.approveTitle", "Approve membership request")}
          </DialogTitle>
          <DialogDescription>
            {t("requests.approveDescription", "Pick the role to grant.")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-4">
          <div className="space-y-2">
            <Label>{t("requests.role", "Role")}</Label>
            <Select value={role} onValueChange={setRole}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="user">user</SelectItem>
                <SelectItem value="viewer">viewer</SelectItem>
                <SelectItem value="admin">admin</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {tc("cancel")}
          </Button>
          <Button onClick={handleSubmit} disabled={approve.isPending}>
            {t("requests.approve", "Approve")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function RejectDialog({
  org,
  uid,
  open,
  onOpenChange,
}: {
  org: string;
  uid: string;
  open: boolean;
  onOpenChange: (b: boolean) => void;
}) {
  const { t } = useTranslation("org");
  const { t: tc } = useTranslation("common");
  const reject = useRejectMembershipRequest(org);
  const [reason, setReason] = useState("");

  const handleSubmit = async () => {
    try {
      await reject.mutateAsync({ uid, reason: reason || undefined });
      toast.success(t("requests.rejected", "Request rejected"));
      onOpenChange(false);
    } catch {
      toast.error(t("requests.rejectFailed", "Failed to reject request"));
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t("requests.rejectTitle", "Reject membership request")}
          </DialogTitle>
          <DialogDescription>
            {t("requests.rejectDescription", "Optionally provide a reason.")}
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4 py-4">
          <div className="space-y-2">
            <Label>{t("requests.reasonLabel", "Reason")}</Label>
            <Textarea
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              rows={3}
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {tc("cancel")}
          </Button>
          <Button onClick={handleSubmit} disabled={reject.isPending}>
            {t("requests.reject", "Reject")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
