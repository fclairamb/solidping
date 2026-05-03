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
import { Loader2, Trash2 } from "lucide-react";
import { useInvitations, useRevokeInvitation } from "@/api/hooks";
import { CreateInvitationDialog } from "@/components/shared/create-invitation-dialog";

export const Route = createFileRoute("/orgs/$org/organization/invitations")({
  component: InvitationsPage,
});

function InvitationsPage() {
  const { t } = useTranslation("org");
  const { t: tc } = useTranslation("common");
  const { org } = Route.useParams();
  const { data: invitationsData, isLoading } = useInvitations(org);
  const revokeInvitation = useRevokeInvitation(org);

  const invitations = invitationsData?.data || [];

  const handleRevoke = async (uid: string) => {
    try {
      await revokeInvitation.mutateAsync(uid);
    } catch {
      // Error handled by react-query
    }
  };

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-end">
        <CreateInvitationDialog org={org} />
      </div>

      <Card>
        <CardHeader>
          <CardTitle>{t("invitations.pendingTitle")}</CardTitle>
          <CardDescription>
            {t("invitations.pendingDescription")}
          </CardDescription>
        </CardHeader>
        <CardContent>
          {isLoading ? (
            <div className="flex items-center justify-center py-8">
              <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
            </div>
          ) : invitations.length === 0 ? (
            <p className="text-center text-muted-foreground py-8">
              {t("invitations.noPending")}
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{tc("email")}</TableHead>
                  <TableHead>{t("invitations.role")}</TableHead>
                  <TableHead>{t("invitations.expires")}</TableHead>
                  <TableHead className="w-[80px]" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {invitations.map((inv) => (
                  <TableRow key={inv.uid}>
                    <TableCell>{inv.email}</TableCell>
                    <TableCell className="capitalize">{inv.role}</TableCell>
                    <TableCell>
                      {new Date(inv.expiresAt).toLocaleDateString()}
                    </TableCell>
                    <TableCell>
                      <Button
                        variant="ghost"
                        size="icon"
                        onClick={() => handleRevoke(inv.uid)}
                        disabled={revokeInvitation.isPending}
                      >
                        <Trash2 className="h-4 w-4 text-destructive" />
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
