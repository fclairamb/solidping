import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Plus, Trash2, Users } from "lucide-react";
import { toast } from "sonner";
import {
  useStatusPageSubscribers,
  useDeleteStatusPageSubscriber,
  useCreateEndpointSubscriber,
  type StatusPageSubscriberChannel,
} from "@/api/hooks";
import { ApiError } from "@/api/client";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
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
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";

interface StatusPageSubscribersProps {
  org: string;
  statusPageUid: string;
}

// StatusPageSubscribers renders a read-only admin list of a status page's
// email subscribers, with a count and a per-row remove action. Subscriber
// emails are PII — managed here only by org admins for their own page.
export function StatusPageSubscribers({
  org,
  statusPageUid,
}: StatusPageSubscribersProps) {
  const { t } = useTranslation("statusPages");
  const { data, isLoading } = useStatusPageSubscribers(org, statusPageUid);
  const deleteSubscriber = useDeleteStatusPageSubscriber(org, statusPageUid);
  const createEndpoint = useCreateEndpointSubscriber(org, statusPageUid);
  const [deleteUid, setDeleteUid] = useState<string | null>(null);
  const [channel, setChannel] =
    useState<Exclude<StatusPageSubscriberChannel, "email">>("webhook");
  const [url, setUrl] = useState("");
  const [secret, setSecret] = useState("");
  const [addError, setAddError] = useState<string | null>(null);
  // The generated signing secret, shown ONCE after a successful add. The API
  // never returns it again, so losing this means re-creating the subscription.
  const [issuedSecret, setIssuedSecret] = useState<string | null>(null);

  const handleAdd = async (event: React.FormEvent) => {
    event.preventDefault();
    setAddError(null);

    try {
      const created = await createEndpoint.mutateAsync({
        channel,
        url: url.trim(),
        // Omitted when the operator left the field empty, so the server
        // generates one and hands it back below.
        ...(secret.trim() === "" ? {} : { signingSecret: secret.trim() }),
      });
      setUrl("");
      // Only surface a secret the operator does NOT already have. One they
      // typed themselves needs no "save this now" banner.
      setIssuedSecret(secret.trim() === "" ? created.data.signingSecret : null);
      setSecret("");
      toast.success(t("subscribers.endpointAdded"));
    } catch (err) {
      const message =
        err instanceof ApiError ? err.message : t("subscribers.endpointAddError");
      setAddError(message);
      toast.error(message);
    }
  };

  const subscribers = data?.subscribers ?? [];
  const count = data?.count ?? 0;

  const handleDelete = async () => {
    if (!deleteUid) return;
    try {
      await deleteSubscriber.mutateAsync(deleteUid);
      toast.success(t("subscribers.removed"));
    } catch {
      toast.error(t("subscribers.removeError"));
    } finally {
      setDeleteUid(null);
    }
  };

  return (
    <Card data-testid="status-page-subscribers">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <Users className="h-5 w-5" />
          {t("subscribers.title")}
          <Badge variant="secondary" data-testid="subscriber-count">
            {count}
          </Badge>
        </CardTitle>
        <CardDescription>{t("subscribers.description")}</CardDescription>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <div className="space-y-2">
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-full" />
          </div>
        ) : subscribers.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            {t("subscribers.empty")}
          </p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("subscribers.destination")}</TableHead>
                <TableHead>{t("subscribers.channel")}</TableHead>
                <TableHead>{t("subscribers.scope")}</TableHead>
                <TableHead>{t("subscribers.status")}</TableHead>
                <TableHead className="text-right">
                  {t("subscribers.actions")}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {subscribers.map((sub) => (
                <TableRow key={sub.uid} data-testid="subscriber-row">
                  <TableCell className="break-all">
                    {/* For an endpoint subscription this is the MASKED URL —
                        the API never returns the real one. */}
                    {sub.email || sub.endpoint || "—"}
                  </TableCell>
                  <TableCell>
                    <Badge variant="outline" data-testid="subscriber-channel">
                      {t(`subscribers.channels.${sub.channel}`, sub.channel)}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    <Badge variant="outline">{sub.scope}</Badge>
                  </TableCell>
                  <TableCell>
                    {sub.disabled ? (
                      /* A disabled delivery is the one state an operator has
                         to act on, so it outranks confirmed/pending here. */
                      <Badge variant="destructive" data-testid="subscriber-disabled">
                        {t("subscribers.disabled")}
                      </Badge>
                    ) : sub.confirmed ? (
                      <Badge variant="success">
                        {t("subscribers.confirmed")}
                      </Badge>
                    ) : (
                      <Badge variant="secondary">
                        {t("subscribers.pending")}
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center justify-end">
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-8 w-8 text-destructive hover:text-destructive"
                        onClick={() => setDeleteUid(sub.uid)}
                        aria-label={t("subscribers.remove")}
                        data-testid="subscriber-row-delete"
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}

        {/* Adding a webhook/Slack delivery is OPERATOR-side: the public
            subscribe form on the status page stays email-only, because a
            visitor pasting an incoming-webhook URL has no verification story.
            The note below says so, so nobody goes looking for the missing
            public control. */}
        <form onSubmit={handleAdd} className="mt-6 space-y-3 border-t pt-6">
          <p className="text-sm font-medium">{t("subscribers.addEndpoint")}</p>
          <p className="text-xs text-muted-foreground">
            {t("subscribers.addEndpointHint")}
          </p>
          {/* Stacks on a phone, side by side from sm up. */}
          <div className="flex flex-col gap-3 sm:flex-row sm:items-end">
            <div className="space-y-2 sm:w-44">
              <Label htmlFor="subscriber-channel">
                {t("subscribers.channel")}
              </Label>
              <Select
                value={channel}
                onValueChange={(v) =>
                  setChannel(v as Exclude<StatusPageSubscriberChannel, "email">)
                }
              >
                <SelectTrigger id="subscriber-channel" data-testid="subscriber-channel-select">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="webhook">
                    {t("subscribers.channels.webhook")}
                  </SelectItem>
                  <SelectItem value="slack">
                    {t("subscribers.channels.slack")}
                  </SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="flex-1 space-y-2">
              <Label htmlFor="subscriber-url">{t("subscribers.url")}</Label>
              <Input
                id="subscriber-url"
                type="url"
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                placeholder={
                  channel === "slack"
                    ? "https://hooks.slack.com/services/…"
                    : "https://example.com/hooks/status"
                }
                data-testid="subscriber-url-input"
              />
            </div>
            <Button
              type="submit"
              disabled={createEndpoint.isPending || url.trim() === ""}
              data-testid="subscriber-add-endpoint"
            >
              <Plus className="mr-2 h-4 w-4" />
              {t("subscribers.add")}
            </Button>
          </div>

          {/* Optional own-secret field. Only meaningful for a generic webhook:
              Slack does not verify signatures, so offering the field there
              would suggest a control that has no effect. */}
          {channel === "webhook" && (
            <div className="space-y-2">
              <Label htmlFor="subscriber-secret">
                {t("subscribers.signingSecret")}
              </Label>
              <Input
                id="subscriber-secret"
                type="text"
                autoComplete="off"
                value={secret}
                onChange={(e) => setSecret(e.target.value)}
                placeholder={t("subscribers.signingSecretPlaceholder")}
                data-testid="subscriber-secret-input"
              />
              <p className="text-xs text-muted-foreground">
                {t("subscribers.signingSecretHint")}
              </p>
            </div>
          )}

          {/* Show-once banner. The server never returns this again, so it is
              deliberately loud and deliberately not auto-dismissed. */}
          {issuedSecret && (
            <Alert data-testid="subscriber-issued-secret">
              <AlertDescription className="space-y-2">
                <p>{t("subscribers.signingSecretIssued")}</p>
                <code className="block break-all rounded bg-muted px-2 py-1 font-mono text-xs">
                  {issuedSecret}
                </code>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => setIssuedSecret(null)}
                  data-testid="subscriber-issued-secret-dismiss"
                >
                  {t("subscribers.signingSecretSaved")}
                </Button>
              </AlertDescription>
            </Alert>
          )}
          {addError && (
            <Alert variant="destructive">
              <AlertDescription data-testid="subscriber-add-error">
                {addError}
              </AlertDescription>
            </Alert>
          )}
        </form>
      </CardContent>

      <AlertDialog
        open={!!deleteUid}
        onOpenChange={(o) => !o && setDeleteUid(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("subscribers.removeConfirmTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("subscribers.removeConfirmDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("subscribers.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={handleDelete}
              disabled={deleteSubscriber.isPending}
            >
              {t("subscribers.remove")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  );
}
