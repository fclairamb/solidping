import { useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { AlertCircle, Check, Loader2, Send, TriangleAlert } from "lucide-react";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useAuth } from "@/contexts/AuthContext";
import { ApiError } from "@/api/client";
import {
  useOperatorNotifications,
  useSaveOperatorNotifications,
  useSendOperatorNoticeTest,
  type OperatorNoticeTestResult,
  type OperatorNotificationsConfig,
} from "@/api/hooks";

export const Route = createFileRoute("/orgs/$org/server/notifications")({
  component: OperatorNotificationsPage,
});

/**
 * Backend event name -> locale key. The event names carry dots
 * ("support.message"), which i18next reads as nested-path separators, so they
 * cannot be locale keys themselves — an unmapped event falls back to its raw
 * name rather than rendering a dotted key path.
 */
const EVENT_LABEL_KEY: Record<string, string> = {
  "support.message": "supportMessage",
  "user.registered": "userRegistered",
};

/** Subscription state as the table edits it: userUid -> set of events. */
type Selection = Record<string, string[]>;

function selectionFrom(config: OperatorNotificationsConfig): Selection {
  const out: Selection = {};
  for (const recipient of config.recipients) {
    out[recipient.userUid] = [...(recipient.events ?? [])];
  }
  return out;
}

function OperatorNotificationsPage() {
  const { t } = useTranslation(["server", "common"]);
  const { org } = Route.useParams();
  // Only the viewer's own row may link to the (per-viewer) notifications page.
  const { user } = useAuth();
  const { data: config, isLoading } = useOperatorNotifications();
  const save = useSaveOperatorNotifications();
  const sendTest = useSendOperatorNoticeTest();

  const [enabled, setEnabled] = useState(false);
  const [selection, setSelection] = useState<Selection>({});
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [testResult, setTestResult] = useState<OperatorNoticeTestResult | null>(
    null,
  );
  const [testError, setTestError] = useState<string | null>(null);

  // Seed the form from the freshly loaded config as a render-phase adjustment
  // (React's documented "adjusting state when props change" pattern) rather
  // than in an effect, which would cause a cascading render.
  const [synced, setSynced] = useState<OperatorNotificationsConfig | undefined>(
    undefined,
  );
  if (config && config !== synced) {
    setSynced(config);
    setEnabled(config.enabled);
    setSelection(selectionFrom(config));
  }

  const toggleEvent = (userUid: string, event: string, checked: boolean) => {
    setSelection((prev) => {
      const current = prev[userUid] ?? [];
      const next = checked
        ? current.includes(event)
          ? current
          : [...current, event]
        : current.filter((e) => e !== event);
      return { ...prev, [userUid]: next };
    });
  };

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSaved(false);
    try {
      await save.mutateAsync({
        enabled,
        recipients: Object.entries(selection).map(([userUid, events]) => ({
          userUid,
          events,
        })),
      });
      setSaved(true);
      setTimeout(() => setSaved(false), 3000);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : t("server:unexpectedError"));
    }
  };

  const handleTest = async () => {
    setTestError(null);
    setTestResult(null);
    try {
      setTestResult(await sendTest.mutateAsync());
    } catch (err) {
      setTestError(
        err instanceof ApiError ? err.message : t("server:unexpectedError"),
      );
    }
  };

  if (isLoading || !config) {
    return (
      <div className="flex items-center justify-center py-12">
        <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
      </div>
    );
  }

  const anySubscribed = Object.values(selection).some((e) => e.length > 0);

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("server:notifications.title")}</CardTitle>
        <CardDescription>
          {t("server:notifications.description")}
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form onSubmit={handleSave} className="space-y-6">
          {error && (
            <Alert variant="destructive">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription data-testid="operator-notifications-error">
                {error}
              </AlertDescription>
            </Alert>
          )}
          {saved && (
            <Alert>
              <Check className="h-4 w-4" />
              <AlertDescription data-testid="operator-notifications-saved">
                {t("server:saved")}
              </AlertDescription>
            </Alert>
          )}

          <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
            <div className="space-y-1">
              <Label htmlFor="operatorNotificationsEnabled">
                {t("server:notifications.enabled")}
              </Label>
              <p className="text-xs text-muted-foreground">
                {t("server:notifications.enabledHelp")}
              </p>
            </div>
            <Switch
              id="operatorNotificationsEnabled"
              data-testid="operator-notifications-enabled"
              checked={enabled}
              onCheckedChange={setEnabled}
              disabled={save.isPending}
            />
          </div>

          {enabled && !anySubscribed && (
            <Alert>
              <TriangleAlert className="h-4 w-4" />
              <AlertDescription data-testid="operator-notifications-no-recipients">
                {t("server:notifications.noRecipients")}
              </AlertDescription>
            </Alert>
          )}

          <div className="overflow-x-auto rounded-md border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("server:notifications.columnUser")}</TableHead>
                  {config.events.map((event) => (
                    <TableHead key={event} className="whitespace-nowrap">
                      {EVENT_LABEL_KEY[event]
                        ? t(
                            `server:notifications.events.${EVENT_LABEL_KEY[event]}`,
                            event,
                          )
                        : event}
                    </TableHead>
                  ))}
                  <TableHead className="hidden sm:table-cell">
                    {t("server:notifications.columnRoutes")}
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {config.recipients.length === 0 ? (
                  <TableRow>
                    <TableCell
                      colSpan={config.events.length + 2}
                      className="py-8 text-center text-sm text-muted-foreground"
                    >
                      {t("server:notifications.noSuperAdmins")}
                    </TableCell>
                  </TableRow>
                ) : (
                  config.recipients.map((recipient) => (
                    <TableRow
                      key={recipient.userUid}
                      data-testid={`operator-recipient-${recipient.email}`}
                    >
                      <TableCell className="max-w-0">
                        <div className="min-w-0">
                          <div className="truncate font-medium">
                            {recipient.email}
                          </div>
                          {recipient.name && (
                            <div className="truncate text-xs text-muted-foreground">
                              {recipient.name}
                            </div>
                          )}
                          {!recipient.superAdmin && (
                            <Badge variant="destructive" className="mt-1">
                              {t("server:notifications.notSuperAdmin")}
                            </Badge>
                          )}
                          {/* The routes summary also lives in the row on
                              mobile, where its own column is hidden — a
                              recipient with no route is the likeliest silent
                              failure and must never be off-screen. */}
                          <div className="mt-1 sm:hidden">
                            <RouteSummary
                              routes={recipient.routes}
                              org={org}
                              warning={t("server:notifications.noRoutes")}
                              selfWarning={t("server:notifications.noRoutesSelf")}
                              isSelf={recipient.userUid === user?.uid}
                            />
                          </div>
                        </div>
                      </TableCell>
                      {config.events.map((event) => (
                        <TableCell key={event}>
                          <Checkbox
                            aria-label={`${recipient.email} ${event}`}
                            data-testid={`operator-event-${recipient.email}-${event}`}
                            checked={(selection[recipient.userUid] ?? []).includes(
                              event,
                            )}
                            onCheckedChange={(value) =>
                              toggleEvent(
                                recipient.userUid,
                                event,
                                value === true,
                              )
                            }
                            disabled={save.isPending}
                          />
                        </TableCell>
                      ))}
                      <TableCell className="hidden sm:table-cell">
                        <RouteSummary
                          routes={recipient.routes}
                          org={org}
                          warning={t("server:notifications.noRoutes")}
                          selfWarning={t("server:notifications.noRoutesSelf")}
                          isSelf={recipient.userUid === user?.uid}
                        />
                      </TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          </div>

          <p className="text-xs text-muted-foreground">
            {t("server:notifications.superAdminOnly")}
          </p>

          <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
            <Button type="submit" disabled={save.isPending}>
              {save.isPending ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                  {t("common:saving")}
                </>
              ) : (
                t("common:save")
              )}
            </Button>
            <Button
              type="button"
              variant="outline"
              data-testid="operator-notifications-test"
              onClick={handleTest}
              disabled={sendTest.isPending}
            >
              {sendTest.isPending ? (
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              ) : (
                <Send className="mr-2 h-4 w-4" />
              )}
              {t("server:notifications.sendTest")}
            </Button>
          </div>

          {testError && (
            <Alert variant="destructive">
              <AlertCircle className="h-4 w-4" />
              <AlertDescription data-testid="operator-notifications-test-result">
                {testError}
              </AlertDescription>
            </Alert>
          )}
          {testResult && (
            <Alert
              variant={testResult.delivered > 0 ? "default" : "destructive"}
            >
              {testResult.delivered > 0 ? (
                <Check className="h-4 w-4" />
              ) : (
                <TriangleAlert className="h-4 w-4" />
              )}
              <AlertDescription data-testid="operator-notifications-test-result">
                {testResult.delivered > 0
                  ? t("server:notifications.testDelivered", {
                      count: testResult.delivered,
                    })
                  : t("server:notifications.testUndeliverable")}
              </AlertDescription>
            </Alert>
          )}
        </form>
      </CardContent>
    </Card>
  );
}

/**
 * The routes cell. A recipient with no enabled notification route is the most
 * likely silent failure of this whole feature — subscribing them looks like it
 * worked and delivers nothing — so it is an amber warning rather than a blank
 * cell.
 *
 * Only the VIEWER'S OWN row links anywhere. Notification routes are per-user
 * and dash0 has no admin route onto somebody else's, so the link that used to
 * sit on every row sent (say) Alice to Alice's settings from Bob's row — which
 * looks actionable, changes nothing about Bob, and is worse than no link at
 * all. Another admin's row therefore states the problem and names who has to
 * fix it: them, on their own notifications page.
 */
function RouteSummary({
  routes,
  org,
  warning,
  selfWarning,
  isSelf,
}: {
  routes: string[];
  org: string;
  warning: string;
  selfWarning: string;
  isSelf: boolean;
}) {
  if (routes.length > 0) {
    return (
      <span className="text-xs text-muted-foreground">{routes.join(", ")}</span>
    );
  }

  const className =
    "inline-flex items-center gap-1 text-xs font-medium text-amber-600 dark:text-amber-500";

  if (isSelf) {
    return (
      <Link
        to="/orgs/$org/account/notifications"
        params={{ org }}
        className={`${className} hover:underline`}
      >
        <TriangleAlert className="h-3.5 w-3.5" />
        {selfWarning}
      </Link>
    );
  }

  return (
    <span className={className}>
      <TriangleAlert className="h-3.5 w-3.5" />
      {warning}
    </span>
  );
}
