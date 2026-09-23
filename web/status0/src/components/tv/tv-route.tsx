import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  isLockedError,
  isNotFoundError,
  useDefaultStatusPage,
  usePublicIncidentHistory,
  usePublicStatusPage,
  usePublicStatusPageSummary,
  type PublicIncident,
  type StatusPage,
} from "@/api/hooks";
import { useLanguageFromPage } from "@/hooks/useLanguageFromPage";
import { captureKioskToken } from "@/lib/kiosk";
import {
  SUMMARY_POLL_MS,
  activeIncidents,
  isStale,
  pollIntervalMs,
  resolveTvState,
  type TvState,
} from "@/lib/tv-board";
import { TvBoard } from "./tv-board";

/**
 * The shared body of every TV route (spec 2026-08-29-08).
 *
 * All three entry points — `/{org}/{slug}/tv`, `/{org}/tv` for the default
 * page, and `/tv` on a custom domain — render this. TV mode is a display mode
 * of an existing status page, not a new thing to configure, so it must resolve
 * a page exactly the way the ordinary routes do and then draw it differently.
 */

/** How often the staleness clock is re-evaluated, in ms. */
const STALE_POLL_MS = 5_000;

/**
 * Tracks whether the board has heard from the API recently enough to be
 * believed.
 *
 * `dataUpdatedAt` from React Query is the timestamp of the last SUCCESSFUL
 * fetch — it does not move on a failed refetch, which is precisely the signal
 * needed. The separate interval exists because nothing else re-renders while
 * polls are failing: without it a board whose network died would sit on its
 * last good render forever, still green.
 */
function useStaleness(lastSuccessAt: number | undefined): boolean {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), STALE_POLL_MS);

    return () => window.clearInterval(timer);
  }, []);

  return isStale(lastSuccessAt || undefined, now);
}

function TvShell({ children }: { children: React.ReactNode }) {
  return (
    <div className="dark flex h-[100dvh] w-full items-center justify-center bg-[oklch(0.19_0.01_250)] px-8 text-center text-[oklch(0.93_0.01_250)]">
      {children}
    </div>
  );
}

/**
 * TvPage renders the board for one resolved page.
 *
 * `slug` is optional: when absent the org's default page is used, which is
 * what `/{org}/tv` addresses.
 */
export function TvPage({ org, slug }: { org: string; slug?: string }) {
  const { t } = useTranslation();

  // Read the kiosk token once, on mount, and strip it from the address bar.
  // Done in an effect-free memo so the very first fetch already carries it —
  // waiting for an effect would fire one tokenless request, which a `private`
  // page answers 404 and which would flash "not found" on the wall.
  const [token] = useState(() => captureKioskToken());

  // The cadence is DERIVED, not stored: React Query accepts a changing
  // refetchInterval, so the board's current state is enough. Keeping it in
  // state would mean one render at the old cadence after every state change,
  // and an effect that exists only to copy a computed value into React.
  const [state, setState] = useState<TvState>("operational");
  const interval = pollIntervalMs(state);

  // TV mode never renders a resource's availability bar or response-time
  // chart (spec 2026-09-22-07) — only `overallStatus`, the counts and the
  // page's own check/group live status, none of which `include` gates.
  // Narrowing to neither section skips both the ~3 MB payload and the
  // server-side query that builds it on every 15-30s poll. It also means the
  // page-level `overallAvailabilityPct` is never populated here: the one
  // number the board did want out of that section now comes from the summary
  // endpoint instead, five minutes apart (spec 2026-09-22-08).
  const slugQuery = usePublicStatusPage(org, slug ?? "", {
    kioskToken: token,
    refetchInterval: interval,
    include: [],
  });
  const defaultQuery = useDefaultStatusPage(slug ? "" : org, {
    kioskToken: token,
    refetchInterval: interval,
    include: [],
  });

  const pageQuery = slug ? slugQuery : defaultQuery;
  const page: StatusPage | undefined = pageQuery.data;

  // The history endpoint has no slug-free variant, so the default-page route
  // can only poll it once the page has resolved its own slug. The slug route —
  // which is what a wallboard URL actually is — has it at mount, so its
  // incidents request leaves at the same instant as the page's and lands
  // roughly 7 s earlier.
  const resolvedSlug = slug ?? page?.slug ?? "";
  const historyQuery = usePublicIncidentHistory(org, resolvedSlug, {
    kioskToken: token,
    // Given an interval for the first time here (spec 2026-08-29-08): the
    // ordinary page fetches this once on mount because a visitor scrolls to
    // it, but a wallboard renders "days since last incident" from it and would
    // otherwise still be counting from a resolution that has since been
    // superseded.
    refetchInterval: interval,
  });

  const incidents: PublicIncident[] | undefined = historyQuery.data?.data;
  // ONE source for the open publications, and it is the fast one (spec
  // 2026-09-22-08). The page payload's own `activeIncidents` is deliberately
  // ignored: reading the two lists off two payloads fetched seconds apart is
  // how the headline and the panel came to disagree.
  const active = activeIncidents(incidents);

  // Derive-during-render rather than in an effect: the cadence has to be right
  // on the render that first learns about an incident, not one render later.
  const liveState = resolveTvState(page?.overallStatus, active);
  if (liveState !== state) setState(liveState);

  // The uptime number, last and slowest. Held back until the page has landed
  // AND says it publishes availability — asking a page that hides it would
  // spend the server's whole availability enrichment on a `null`.
  const summaryQuery = usePublicStatusPageSummary(org, resolvedSlug, {
    kioskToken: token,
    enabled: page?.showAvailability === true,
    refetchInterval: SUMMARY_POLL_MS,
  });

  useLanguageFromPage(page?.language);

  const lastSuccessAt = pageQuery.dataUpdatedAt || undefined;
  const stale = useStaleness(lastSuccessAt);

  // A locked page on a TV means the URL is missing (or carrying a dead) kiosk
  // token. There is deliberately NO password form here: nobody is standing at
  // the screen to fill one in, and rendering an input on a wallboard would
  // just be an unusable box. Say what an operator has to do instead.
  //
  // Checked on BOTH reads, and ahead of the loading shell: the page and the
  // incidents are behind one visibility gate on the server, so whichever
  // answers 401 first is already the truth about the whole screen. Waiting for
  // the other one would only delay the sentence an operator needs.
  if (isLockedError(pageQuery.error) || isLockedError(historyQuery.error)) {
    return (
      <TvShell>
        <div data-testid="tv-locked">
          <h1 className="text-4xl font-bold">{t("tv.lockedTitle")}</h1>
          <p className="mt-3 text-2xl opacity-75">
            {t("tv.lockedDescription")}
          </p>
        </div>
      </TvShell>
    );
  }

  // Never render a confident board over a read that FAILED and has nothing
  // cached. Same rule as before for the page; extended to the incidents,
  // because the board now renders off that read alone and a 404 there is the
  // same fact about the same page — the URL on the wall is wrong.
  //
  // The incidents side is restricted to a 404 on purpose. The page side keeps
  // the older, broader rule (any error with no data at all), but promoting a
  // 5xx on the SECOND read to a full-screen "not found" would replace a board
  // that is rendering correctly with a false statement about the page.
  //
  // A query that merely has not answered yet is not this case — it has no
  // error — and a query that failed AFTER succeeding once still has its data,
  // which is what keeps one bad poll from collapsing the board into "not
  // found" instead of greying it.
  const pageFailed = pageQuery.error !== null && page === undefined;
  const incidentsFailed =
    isNotFoundError(historyQuery.error) && incidents === undefined;

  if (pageFailed || incidentsFailed) {
    return (
      <TvShell>
        <div data-testid="tv-not-found">
          <h1 className="text-4xl font-bold">{t("statusPageNotFound")}</h1>
          <p className="mt-3 text-2xl opacity-75">
            {t("statusPageNotFoundDescription")}
          </p>
        </div>
      </TvShell>
    );
  }

  // Nothing at all yet. The board goes up as soon as EITHER read lands —
  // incidents first in practice, which is the whole point of this spec — and
  // renders only what that read can honestly say.
  if (page === undefined && incidents === undefined) {
    return (
      <TvShell>
        <p className="text-3xl opacity-70">{t("loading")}</p>
      </TvShell>
    );
  }

  return (
    <TvBoard
      page={page}
      incidents={incidents}
      availabilityPct={summaryQuery.data?.overallAvailabilityPct}
      stale={stale}
      lastUpdatedAt={lastSuccessAt}
    />
  );
}

/**
 * TvNotConfigured is what `/tv` shows on the installation's own host, where
 * there is no custom domain to resolve a page from. It names the URL that does
 * work rather than 404ing silently — a blank TV tells an operator nothing.
 */
export function TvNotConfigured() {
  const { t } = useTranslation();

  return (
    <TvShell>
      <div>
        <h1 className="text-4xl font-bold">{t("tv.pickAPageTitle")}</h1>
        <p className="mt-3 text-2xl opacity-75">
          {t("tv.pickAPageDescription")}
        </p>
      </div>
    </TvShell>
  );
}
