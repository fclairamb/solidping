import { useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import {
  useCreateEventFeed,
  useEventFeeds,
  useRevokeEventFeed,
  useRotateEventFeed,
  type EventFeedSecret,
} from "@/api/hooks";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { CopyableCode } from "@/components/shared/copyable-code";
import { DocsLink } from "@/components/shared/docs-link";
import { scopeKey } from "@/lib/event-feeds";
import { formatRelativeTime } from "@/lib/time-ago";

/**
 * "Event feeds" (spec 2026-11-24-12): private Atom feeds of the org's
 * incidents and events, for feed readers, NOC walls and automation. The URL
 * is the credential, so it is shown once (create, rotate) and kept out of
 * session replay. Admin-only: the settings page already is, and the server
 * enforces it with RequireOrgAdmin.
 */
export function EventFeedsCard({ org }: { org: string }) {
  const { t } = useTranslation("org");
  const { t: tc } = useTranslation("common");
  const { data } = useEventFeeds(org);
  const createFeed = useCreateEventFeed(org);
  const rotateFeed = useRotateEventFeed(org);
  const revokeFeed = useRevokeEventFeed(org);

  const [name, setName] = useState("");
  const [scope, setScope] = useState<string[] | null>(null);
  const [revealed, setRevealed] = useState<EventFeedSecret | null>(null);

  const feeds = data?.data ?? [];
  const choices = data?.scopeChoices ?? [];
  const selected = scope ?? data?.defaultScope ?? [];
  const atCap = data ? feeds.length >= data.maxFeeds : false;

  const toggle = (choice: string, on: boolean) => {
    const next = on ? [...selected, choice] : selected.filter((item) => item !== choice);
    setScope(next);
  };

  const onCreate = async () => {
    try {
      const created = await createFeed.mutateAsync({ name: name.trim(), eventTypes: selected });
      setRevealed(created);
      setName("");
      setScope(null);
    } catch {
      toast.error(t("settings.eventFeeds.createFailed"));
    }
  };

  const onRotate = async (uid: string) => {
    if (!confirm(t("settings.eventFeeds.rotateConfirm"))) return;
    try {
      setRevealed(await rotateFeed.mutateAsync(uid));
    } catch {
      toast.error(t("settings.eventFeeds.rotateFailed"));
    }
  };

  const onRevoke = async (uid: string) => {
    if (!confirm(t("settings.eventFeeds.revokeConfirm"))) return;
    try {
      await revokeFeed.mutateAsync(uid);
      if (revealed?.uid === uid) setRevealed(null);
      toast.success(t("settings.eventFeeds.revoked"));
    } catch {
      toast.error(t("settings.eventFeeds.revokeFailed"));
    }
  };

  return (
    <Card className="mt-6" data-testid="event-feeds-card">
      <CardHeader>
        <CardTitle>{t("settings.eventFeeds.title")}</CardTitle>
        <CardDescription>
          {t("settings.eventFeeds.description")}{" "}
          <DocsLink href="/docs/features/events#private-atom-feed" />
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {revealed && (
          <Alert data-testid="event-feed-secret">
            <AlertDescription className="space-y-2">
              <p>{t("settings.eventFeeds.copyOnce", { name: revealed.name })}</p>
              <CopyableCode code={revealed.url} secret data-testid="event-feed-url" />
              <Button
                variant="outline"
                size="sm"
                onClick={() => setRevealed(null)}
                data-testid="event-feed-secret-dismiss"
              >
                {t("settings.eventFeeds.done")}
              </Button>
            </AlertDescription>
          </Alert>
        )}

        {feeds.length === 0 ? (
          <p className="text-sm text-muted-foreground" data-testid="event-feeds-empty">
            {t("settings.eventFeeds.empty")}
          </p>
        ) : (
          <ul className="divide-y rounded-md border" data-testid="event-feeds-list">
            {feeds.map((feed) => (
              <li
                key={feed.uid}
                className="flex flex-wrap items-center justify-between gap-2 p-3"
                data-testid={`event-feed-item-${feed.uid}`}
              >
                <div className="min-w-0 space-y-1">
                  <p className="font-medium" data-testid="event-feed-name">
                    {feed.name}
                  </p>
                  <p className="text-xs text-muted-foreground">
                    {feed.eventTypes.map((item) => t(`settings.eventFeeds.scope.${scopeKey(item)}`)).join(", ")}
                  </p>
                  <p className="text-xs text-muted-foreground" data-testid="event-feed-last-used">
                    {t("settings.eventFeeds.lastUsed")}:{" "}
                    {feed.lastUsedAt
                      ? formatRelativeTime(new Date(feed.lastUsedAt), tc)
                      : t("settings.eventFeeds.never")}
                  </p>
                </div>
                <div className="flex gap-2">
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => onRotate(feed.uid)}
                    disabled={rotateFeed.isPending}
                    data-testid="event-feed-rotate"
                  >
                    {t("settings.eventFeeds.rotate")}
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => onRevoke(feed.uid)}
                    disabled={revokeFeed.isPending}
                    data-testid="event-feed-revoke"
                  >
                    {t("settings.eventFeeds.revoke")}
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        )}

        <form
          className="space-y-3"
          data-testid="event-feed-form"
          onSubmit={(event) => {
            event.preventDefault();
            void onCreate();
          }}
        >
          <div className="space-y-2">
            <Label htmlFor="event-feed-name">{t("settings.eventFeeds.name")}</Label>
            <Input
              id="event-feed-name"
              value={name}
              maxLength={100}
              placeholder={t("settings.eventFeeds.namePlaceholder")}
              onChange={(event) => setName(event.target.value)}
              className="w-full sm:w-80"
              data-testid="event-feed-name-input"
            />
          </div>
          <fieldset className="space-y-2">
            <legend className="text-sm font-medium">{t("settings.eventFeeds.scopeLabel")}</legend>
            {choices.map((choice) => (
              <label
                key={choice}
                className="flex items-center gap-2 text-sm"
                data-testid={`event-feed-scope-${scopeKey(choice)}`}
              >
                <Checkbox
                  data-testid={`event-feed-scope-checkbox-${scopeKey(choice)}`}
                  checked={selected.includes(choice)}
                  onCheckedChange={(checked) => toggle(choice, checked === true)}
                />
                {t(`settings.eventFeeds.scope.${scopeKey(choice)}`)}
              </label>
            ))}
          </fieldset>
          {atCap && (
            <p className="text-xs text-muted-foreground" data-testid="event-feeds-cap">
              {t("settings.eventFeeds.cap", { count: data?.maxFeeds })}
            </p>
          )}
          <Button
            type="submit"
            disabled={!name.trim() || selected.length === 0 || atCap || createFeed.isPending}
            data-testid="event-feed-create"
          >
            {t("settings.eventFeeds.create")}
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}
