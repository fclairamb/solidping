import { createFileRoute, Link } from "@tanstack/react-router";
import { BellRing, Settings } from "lucide-react";
import { useMyNotifications, type IncidentNotification } from "@/api/hooks";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { PageHeader } from "@/components/shared/page-header";
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
import { useTranslation } from "react-i18next";
import { channelTypeLabel, failureReasonLabel } from "@/lib/channel-labels";

export const Route = createFileRoute("/orgs/$org/me/notifications")({
  component: MyNotificationsPage,
});

function statusVariant(
  status: IncidentNotification["status"],
): "default" | "secondary" | "destructive" | "outline" {
  switch (status) {
    case "sent":
      return "default";
    case "failed":
      return "destructive";
    case "pending":
      return "outline";
    case "skipped":
    case "cancelled":
    default:
      return "secondary";
  }
}

function MyNotificationsPage() {
  const { t } = useTranslation("common");
  const { org } = Route.useParams();
  const { data: rows, isLoading } = useMyNotifications(org, { limit: 100 });

  return (
    <div className="space-y-6" data-testid="my-notifications-page">
      <PageHeader
        icon={BellRing}
        title="My pages"
        description="Incidents you were paged for, in reverse chronological order."
        actions={
          <Button asChild variant="outline" aria-label="Notification settings">
            <Link to="/orgs/$org/account/notifications" params={{ org }}>
              <Settings />
              <span className="hidden sm:inline">Notification settings</span>
            </Link>
          </Button>
        }
        className="flex-wrap"
      />

      <Card>
        <CardHeader>
          <CardTitle>Notifications</CardTitle>
          <CardDescription>
            Every time you were paged for an incident.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {isLoading && <Skeleton className="h-32 w-full" />}

          {!isLoading && (!rows || rows.length === 0) && (
            <div className="space-y-3 py-6 text-center">
              <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-muted">
                <BellRing className="h-6 w-6 text-muted-foreground" />
              </div>
              <p className="text-sm font-medium text-foreground">
                {t("common:myNotifications.empty.title")}
              </p>
              <p className="mx-auto max-w-sm text-xs text-muted-foreground">
                {t("common:myNotifications.empty.hint")}
              </p>
              <Button asChild size="sm" variant="outline">
                <Link to="/orgs/$org/account/notifications" params={{ org }}>
                  <Settings className="mr-1.5 h-3.5 w-3.5" />
                  {t("common:myNotifications.empty.cta")}
                </Link>
              </Button>
            </div>
          )}

          {!isLoading && rows && rows.length > 0 && (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Time</TableHead>
                  <TableHead>Incident</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Channel</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((row) => (
                  <TableRow key={row.uid}>
                    <TableCell
                      className="text-sm whitespace-nowrap text-muted-foreground"
                      title={row.createdAt}
                    >
                      {new Date(row.createdAt).toLocaleString()}
                    </TableCell>
                    <TableCell className="text-sm">
                      {row.incident ? (
                        <Link
                          to="/orgs/$org/incidents/$incidentUid"
                          params={{ org, incidentUid: row.incidentUid }}
                          className="text-primary hover:underline"
                        >
                          {row.incident.title || row.incidentUid}
                        </Link>
                      ) : (
                        <Link
                          to="/orgs/$org/incidents/$incidentUid"
                          params={{ org, incidentUid: row.incidentUid }}
                          className="text-primary hover:underline"
                        >
                          {row.incidentUid}
                        </Link>
                      )}
                      {row.incident && (
                        <Badge
                          variant={
                            row.incident.state === "active"
                              ? "destructive"
                              : "secondary"
                          }
                          className="ml-2 text-xs"
                        >
                          {row.incident.state}
                        </Badge>
                      )}
                    </TableCell>
                    <TableCell>
                      <Badge
                        variant={statusVariant(row.status)}
                        className="text-xs capitalize"
                      >
                        {row.status}
                      </Badge>
                      {/*
                        Why a delivery failed matters more than that it failed:
                        "recipient not on WhatsApp" is a user action, a paused
                        template is an admin action.
                      */}
                      {(row.skipReason || row.error) && (
                        <p
                          className="mt-1 text-xs text-muted-foreground break-words"
                          data-testid={`notification-reason-${row.uid}`}
                        >
                          {failureReasonLabel(t, row.skipReason) || row.error}
                        </p>
                      )}
                    </TableCell>
                    <TableCell className="text-sm">
                      {channelTypeLabel(t, row.channelType)}
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
