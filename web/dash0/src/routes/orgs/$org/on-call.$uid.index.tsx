import { useMemo, useState } from "react";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { ArrowLeft, Calendar, Pencil, Trash2 } from "lucide-react";

import {
  useOnCallSchedule,
  useOnCallSchedulePreview,
  useOnCallScheduleOverrides,
  useDeleteOnCallSchedule,
  useDeleteOnCallOverride,
  useEnableOnCallICalFeed,
  useDisableOnCallICalFeed,
  useRotateOnCallICalFeed,
  useMembers,
} from "@/api/hooks";
import {
  EmailOnlyBadge,
  useEmailOnlyUserUids,
} from "@/components/notifications/member-coverage";
import { Badge } from "@/components/ui/badge";
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
import { QueryErrorView } from "@/components/shared/error-views";

export const Route = createFileRoute("/orgs/$org/on-call/$uid/")({
  component: OnCallDetailPage,
});

const COLOR_PALETTE = [
  "bg-blue-500",
  "bg-emerald-500",
  "bg-amber-500",
  "bg-rose-500",
  "bg-violet-500",
  "bg-cyan-500",
  "bg-orange-500",
  "bg-pink-500",
];

function OnCallDetailPage() {
  const { t } = useTranslation(["oncall", "common"]);
  const { org, uid } = Route.useParams();
  const navigate = useNavigate();

  const {
    data: schedule,
    isLoading,
    error,
    refetch,
  } = useOnCallSchedule(org, uid);
  const { data: slots } = useOnCallSchedulePreview(org, uid);
  const { data: overrides } = useOnCallScheduleOverrides(org, uid);
  const { data: membersResp } = useMembers(org);

  const deleteMutation = useDeleteOnCallSchedule(org);
  const deleteOverride = useDeleteOnCallOverride(org, uid);
  const enableFeed = useEnableOnCallICalFeed(org, uid);
  const disableFeed = useDisableOnCallICalFeed(org, uid);
  const rotateFeed = useRotateOnCallICalFeed(org, uid);
  const [feedURL, setFeedURL] = useState<string | null>(null);
  const [deleteOpen, setDeleteOpen] = useState(false);

  // Same signal as the schedule editor: this page is where an admin reviews who
  // is actually on the hook, so a rostered member nothing but email can reach
  // has to be visible here too — not just while editing the roster.
  const emailOnlyUsers = useEmailOnlyUserUids(org);

  const userByUID = useMemo(() => {
    const m = new Map<string, string>();
    for (const member of membersResp?.data ?? []) {
      m.set(member.userUid, member.name || member.email);
    }
    return m;
  }, [membersResp]);

  if (error) {
    return (
      <QueryErrorView
        error={error}
        org={org}
        resource={t("oncall:list.title")}
        onRetry={() => refetch()}
      />
    );
  }

  if (isLoading || !schedule) {
    return <div className="p-6 text-muted-foreground">{t("oncall:detail.previewLoading")}</div>;
  }

  const handleConfirmDelete = async () => {
    await deleteMutation.mutateAsync(uid);
    setDeleteOpen(false);
    navigate({ to: "/orgs/$org/on-call", params: { org } });
  };

  const handleEnableFeed = async () => {
    const result = await enableFeed.mutateAsync();
    setFeedURL(result.url);
  };

  const handleRotateFeed = async () => {
    if (!confirm(t("oncall:detail.ical.rotateConfirmBody"))) return;
    const result = await rotateFeed.mutateAsync();
    setFeedURL(result.url);
  };

  const handleDisableFeed = async () => {
    await disableFeed.mutateAsync();
    setFeedURL(null);
  };

  return (
    <div className="space-y-6">
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">{schedule.name}</h1>
          <p className="text-muted-foreground text-sm">
            {schedule.timezone} ·{" "}
            {schedule.rotationType === "daily"
              ? t("oncall:detail.handoffDaily", { time: schedule.handoffTime })
              : t("oncall:detail.handoffWeekly", {
                  weekday: t(
                    `oncall:form.weekday${(["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"] as const)[
                      schedule.handoffWeekday ?? 0
                    ]}`,
                  ),
                  time: schedule.handoffTime,
                })}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            asChild
            variant="ghost"
            size="icon"
            aria-label={t("oncall:detail.back")}
          >
            <Link to="/orgs/$org/on-call" params={{ org }}>
              <ArrowLeft />
            </Link>
          </Button>
          <Button asChild variant="outline" aria-label={t("oncall:detail.edit")}>
            <Link
              to="/orgs/$org/on-call/$uid/edit"
              params={{ org, uid }}
              data-testid="oncall-edit-button"
            >
              <Pencil />
              <span className="hidden sm:inline">{t("oncall:detail.edit")}</span>
            </Link>
          </Button>
          <Button
            variant="destructive"
            aria-label={t("oncall:detail.delete")}
            onClick={() => setDeleteOpen(true)}
          >
            <Trash2 />
            <span className="hidden sm:inline">{t("oncall:detail.delete")}</span>
          </Button>
        </div>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Calendar className="h-5 w-5" />
            {t("oncall:list.currentlyOnCall")}
          </CardTitle>
        </CardHeader>
        <CardContent>
          {schedule.currentlyOnCall ? (
            <p className="text-2xl font-medium">{schedule.currentlyOnCall.name}</p>
          ) : (
            <p className="text-muted-foreground">{t("oncall:list.noOneOnCall")}</p>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("oncall:detail.previewTitle")}</CardTitle>
        </CardHeader>
        <CardContent>
          {!slots || slots.length === 0 ? (
            <p className="text-muted-foreground text-sm">
              {t("oncall:detail.noUsers")}
            </p>
          ) : (
            <CalendarStrip slots={slots} userByUID={userByUID} />
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("oncall:detail.users")}</CardTitle>
        </CardHeader>
        <CardContent>
          {!schedule.userUids || schedule.userUids.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              {t("oncall:detail.noUsers")}
            </p>
          ) : (
            <ol className="space-y-1">
              {schedule.userUids.map((uid, idx) => (
                <li
                  key={uid}
                  // flex-wrap so the badge drops to its own line on narrow
                  // screens instead of squeezing the name off the row.
                  className="flex flex-wrap items-center gap-2 text-sm"
                >
                  <span className="text-muted-foreground w-6 text-right">{idx + 1}.</span>
                  <span>{userByUID.get(uid) ?? uid}</span>
                  {emailOnlyUsers.has(uid) && <EmailOnlyBadge />}
                </li>
              ))}
            </ol>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("oncall:detail.overrides")}</CardTitle>
          <CardDescription>{t("oncall:detail.overrides")}</CardDescription>
        </CardHeader>
        <CardContent>
          {!overrides || overrides.length === 0 ? (
            <p className="text-sm text-muted-foreground py-2">
              {t("oncall:detail.noOverrides")}
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("oncall:list.currentlyOnCall")}</TableHead>
                  <TableHead>{t("oncall:detail.overrideRange", { from: "from", to: "to" })}</TableHead>
                  <TableHead className="w-12" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {overrides.map((o) => (
                  <TableRow key={o.uid}>
                    <TableCell>{userByUID.get(o.userUid) ?? o.userUid}</TableCell>
                    <TableCell className="text-sm text-muted-foreground">
                      {new Date(o.startAt).toLocaleString()} →{" "}
                      {new Date(o.endAt).toLocaleString()}
                    </TableCell>
                    <TableCell>
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => deleteOverride.mutate(o.uid)}
                        aria-label={t("oncall:detail.removeOverride")}
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("oncall:detail.ical.title")}</CardTitle>
          <CardDescription>{t("oncall:detail.ical.description")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          {schedule.icalEnabled || feedURL ? (
            <>
              {feedURL && (
                <div className="space-y-1">
                  <p className="text-xs text-muted-foreground">
                    {t("oncall:detail.ical.url")}
                  </p>
                  <code className="block text-xs bg-muted p-2 rounded break-all">
                    {feedURL}
                  </code>
                </div>
              )}
              <div className="flex gap-2">
                <Button variant="outline" size="sm" onClick={handleRotateFeed}>
                  {t("oncall:detail.ical.rotate")}
                </Button>
                <Button variant="outline" size="sm" onClick={handleDisableFeed}>
                  {t("oncall:detail.ical.disable")}
                </Button>
              </div>
            </>
          ) : (
            <Button onClick={handleEnableFeed} disabled={enableFeed.isPending}>
              {t("oncall:detail.ical.enable")}
            </Button>
          )}
        </CardContent>
      </Card>

      <p className="text-xs text-muted-foreground italic">
        {t("oncall:detail.v1Notes")}
      </p>

      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("oncall:detail.deleteConfirmTitle", { name: schedule.name })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("oncall:detail.deleteConfirmBody")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("oncall:form.cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={handleConfirmDelete}>
              {t("oncall:detail.deleteConfirmAction")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

interface CalendarStripProps {
  slots: { userUid: string; from: string; to: string }[];
  userByUID: Map<string, string>;
}

function CalendarStrip({ slots, userByUID }: CalendarStripProps) {
  const colorByUser = useMemo(() => {
    const m = new Map<string, string>();
    let idx = 0;
    for (const slot of slots) {
      if (!m.has(slot.userUid)) {
        m.set(slot.userUid, COLOR_PALETTE[idx % COLOR_PALETTE.length]);
        idx++;
      }
    }
    return m;
  }, [slots]);

  const totalMs = useMemo(() => {
    if (slots.length === 0) return 1;
    const start = new Date(slots[0].from).getTime();
    const end = new Date(slots[slots.length - 1].to).getTime();
    return end - start || 1;
  }, [slots]);

  const startMs = slots.length > 0 ? new Date(slots[0].from).getTime() : 0;

  return (
    <div className="space-y-3">
      <div className="flex h-10 rounded overflow-hidden border">
        {slots.map((slot, idx) => {
          const slotStart = new Date(slot.from).getTime();
          const slotEnd = new Date(slot.to).getTime();
          const widthPct = ((slotEnd - slotStart) / totalMs) * 100;
          return (
            <div
              key={`${slot.userUid}-${idx}-${slot.from}`}
              className={`${colorByUser.get(slot.userUid) ?? "bg-muted"} text-xs text-white flex items-center justify-center overflow-hidden whitespace-nowrap`}
              style={{ width: `${widthPct}%` }}
              title={`${userByUID.get(slot.userUid) ?? slot.userUid}: ${new Date(slot.from).toLocaleString()} → ${new Date(slot.to).toLocaleString()}`}
            >
              {widthPct > 8 ? (userByUID.get(slot.userUid) ?? slot.userUid) : ""}
            </div>
          );
        })}
      </div>

      <div className="flex flex-wrap gap-3 text-xs">
        {Array.from(colorByUser.entries()).map(([uid, color]) => (
          <div key={uid} className="flex items-center gap-1">
            <span className={`inline-block w-3 h-3 rounded ${color}`} />
            <span>{userByUID.get(uid) ?? uid}</span>
          </div>
        ))}
      </div>

      <Badge variant="outline" className="text-xs">
        {new Date(startMs).toLocaleDateString()} →{" "}
        {new Date(startMs + totalMs).toLocaleDateString()}
      </Badge>
    </div>
  );
}
