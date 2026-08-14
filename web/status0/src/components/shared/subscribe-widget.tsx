import { useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { Rss } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useSubscribe } from "@/api/hooks";

interface SubscribeWidgetProps {
  org: string;
  statusPageUid: string;
  feedUrl: string;
}

// SubscribeWidget lets a visitor subscribe to status updates by email
// (double opt-in: they receive a confirmation mail) and links to the Atom feed.
export function SubscribeWidget({
  org,
  statusPageUid,
  feedUrl,
}: SubscribeWidgetProps) {
  const { t } = useTranslation();
  const [email, setEmail] = useState("");
  const subscribe = useSubscribe();

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (!email.trim()) return;
    subscribe.mutate({ org, statusPageUid, email: email.trim() });
  };

  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-lg">{t("subscribe.title")}</CardTitle>
      </CardHeader>
      <CardContent>
        {subscribe.isSuccess ? (
          <div
            role="status"
            // Same soft-tint recipe as the success badge variant
            // (components/ui/badge.tsx) — a --status-ok-themed surface
            // instead of hardcoded green-*/dark:green-* literals, so it
            // tracks the palette automatically instead of needing its own
            // dark-mode pair.
            className="rounded-md border border-status-ok/25 bg-status-ok/15 px-4 py-3 text-sm text-status-ok-foreground"
          >
            {t("subscribe.checkInbox")}
          </div>
        ) : (
          <form
            onSubmit={onSubmit}
            className="flex flex-col gap-3 sm:flex-row sm:items-start"
          >
            <div className="flex-1">
              <label htmlFor="subscribe-email" className="sr-only">
                {t("subscribe.emailLabel")}
              </label>
              <input
                id="subscribe-email"
                type="email"
                required
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                placeholder={t("subscribe.emailPlaceholder")}
                className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
                disabled={subscribe.isPending}
              />
              {subscribe.isError && (
                // --destructive (not a hardcoded red-*) so this stays legible
                // and on-brand in dark mode like every other destructive
                // surface in the app.
                <p className="mt-1 text-sm text-destructive">
                  {t("subscribe.error")}
                </p>
              )}
            </div>
            {/* translate="no" — the label swaps between two strings while the
                button element itself is reused, so React rewrites this text
                node. A translator that has re-parented it into a <font> turns
                that into "removeChild on Node". See NO_TRANSLATE in
                status-page-view.tsx. */}
            <button
              type="submit"
              disabled={subscribe.isPending}
              className="inline-flex items-center justify-center rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
              data-testid="subscribe-submit"
              translate="no"
            >
              {subscribe.isPending
                ? t("subscribe.submitting")
                : t("subscribe.submit")}
            </button>
          </form>
        )}

        <div className="mt-3">
          <a
            href={feedUrl}
            target="_blank"
            rel="noreferrer noopener"
            className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
          >
            <Rss className="h-4 w-4" />
            {t("subscribe.feedLink")}
          </a>
        </div>
      </CardContent>
    </Card>
  );
}
