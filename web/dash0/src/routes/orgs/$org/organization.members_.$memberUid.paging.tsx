import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { createFileRoute, Link } from "@tanstack/react-router";
import { toast } from "sonner";
import { ArrowLeft, Loader2, Mail, ShieldAlert } from "lucide-react";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApiError } from "@/api/client";
import {
  useMembers,
  useMemberCoverage,
  useAddMemberContact,
  useSendPagingNudge,
} from "@/api/hooks";
import { PagingCoverageCell } from "@/components/notifications/member-coverage";

export const Route = createFileRoute(
  "/orgs/$org/organization/members_/$memberUid/paging",
)({
  component: MemberPagingPage,
});

type ProvisionableType = "phone" | "whatsapp";

/**
 * Admin pre-provisioning for one member (spec 2026-08-12-03, phase 3).
 *
 * A dedicated route rather than a modal, per the dashboard convention. The page
 * is explicit that what it creates is *unverified*: the admin saves their
 * colleague the typing, the colleague still has to prove the number is theirs
 * before anything pages it.
 */
function MemberPagingPage() {
  const { t } = useTranslation("org");
  const { t: tc } = useTranslation("common");
  const { org, memberUid } = Route.useParams();

  const { data: membersData, isLoading } = useMembers(org);
  const { data: coverageData } = useMemberCoverage(org);
  const addContact = useAddMemberContact(org);
  const nudge = useSendPagingNudge(org);

  const [type, setType] = useState<ProvisionableType>("phone");
  const [value, setValue] = useState("");

  const member = useMemo(
    () => (membersData?.data ?? []).find((m) => m.uid === memberUid),
    [membersData, memberUid],
  );

  const coverage = useMemo(
    () => (coverageData?.data ?? []).find((c) => c.userUid === member?.userUid),
    [coverageData, member],
  );

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();

    try {
      await addContact.mutateAsync({ memberUid, type, value: value.trim() });
      setValue("");
      toast.success(t("members.paging.added"));
    } catch (err) {
      toast.error(
        err instanceof ApiError ? err.message : t("members.paging.addFailed"),
      );
    }
  };

  const sendNudge = async () => {
    try {
      await nudge.mutateAsync(memberUid);
      toast.success(t("members.paging.nudgeSent"));
    } catch (err) {
      toast.error(
        err instanceof ApiError ? err.message : t("members.paging.nudgeFailed"),
      );
    }
  };

  return (
    <TooltipProvider>
      <div className="space-y-6">
        <Button variant="ghost" size="sm" asChild className="-ml-2">
          <Link to="/orgs/$org/organization/members" params={{ org }}>
            <ArrowLeft className="mr-2 h-4 w-4" />
            {t("members.paging.backToMembers", "Back to members")}
          </Link>
        </Button>

        <Card>
          <CardHeader>
            <CardTitle>{t("members.paging.title")}</CardTitle>
            <CardDescription>
              {isLoading
                ? tc("loading")
                : (member?.name ?? member?.email ?? memberUid)}
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-6">
            <div className="space-y-2">
              <Label>{t("members.paging.currentCoverage")}</Label>
              <PagingCoverageCell coverage={coverage} />
            </div>

            <Alert>
              <ShieldAlert className="h-4 w-4" />
              <AlertTitle>{t("members.paging.unverifiedTitle")}</AlertTitle>
              <AlertDescription>
                {t("members.paging.unverifiedBody")}
              </AlertDescription>
            </Alert>

            <form onSubmit={submit} className="space-y-4">
              <div className="grid gap-4 sm:grid-cols-[160px_1fr]">
                <div className="space-y-2">
                  <Label htmlFor="paging-type">
                    {t("members.paging.type")}
                  </Label>
                  <Select
                    value={type}
                    onValueChange={(v) => setType(v as ProvisionableType)}
                  >
                    <SelectTrigger id="paging-type" data-testid="paging-type">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="phone">
                        {t("members.paging.typePhone")}
                      </SelectItem>
                      <SelectItem value="whatsapp">
                        {t("members.paging.typeWhatsApp")}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="paging-value">
                    {t("members.paging.number")}
                  </Label>
                  <Input
                    id="paging-value"
                    value={value}
                    onChange={(e) => setValue(e.target.value)}
                    placeholder="+15551234567"
                    data-testid="paging-value"
                  />
                </div>
              </div>
              <Button
                type="submit"
                disabled={!value.trim() || addContact.isPending}
                data-testid="paging-submit"
              >
                {addContact.isPending && (
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                )}
                {t("members.paging.add")}
              </Button>
            </form>

            <div className="rounded border p-3 space-y-2">
              <p className="font-medium">{t("members.paging.nudgeTitle")}</p>
              <p className="text-xs text-muted-foreground">
                {t("members.paging.nudgeBody")}
              </p>
              <Button
                type="button"
                variant="outline"
                onClick={sendNudge}
                disabled={nudge.isPending}
                data-testid="paging-nudge"
              >
                {nudge.isPending ? (
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                ) : (
                  <Mail className="mr-2 h-4 w-4" />
                )}
                {t("members.paging.nudgeAction")}
              </Button>
            </div>
          </CardContent>
        </Card>
      </div>
    </TooltipProvider>
  );
}
