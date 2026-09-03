import { useTranslation } from "react-i18next";
import { Link } from "@tanstack/react-router";
import { AlertTriangle } from "lucide-react";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import type { StalePublication } from "@/api/hooks";
import { groupStaleByPage } from "@/lib/stale-publications";

interface StalePublicationsBannerProps {
  org: string;
  /** Open publications whose linked incident has already resolved. */
  stale: StalePublication[];
}

/**
 * StalePublicationsBanner tells an operator that their public status page is
 * still announcing an incident that recovered (spec 2026-09-02-05).
 *
 * Nothing else in dash0 can say this. The checks list knows nothing about
 * publications; the incident behind it is resolved, so the incidents list has
 * moved on; and the only surfaces that still show the entry — the public page
 * and the wallboard — are the ones the operator is not looking at when they
 * wonder why the TV is amber. The reported case sat unnoticed for ten days.
 *
 * A warning, never destructive: nothing is broken and nothing is lost, there
 * is simply a stale public claim, and closing it is a one-click action on the
 * route this banner links to. Renders nothing when every page is clean.
 */
export function StalePublicationsBanner({
  org,
  stale,
}: StalePublicationsBannerProps) {
  const { t } = useTranslation(["statusPages"]);
  const groups = groupStaleByPage(stale);

  if (groups.length === 0) {
    return null;
  }

  return (
    <Alert variant="warning" data-testid="stale-publications-banner">
      <AlertTriangle />
      <AlertTitle>{t("statusPages:stalePublications.title")}</AlertTitle>
      <AlertDescription className="space-y-2">
        {groups.map((group) => (
          <div
            key={group.pageUid}
            className="flex flex-wrap items-center gap-x-3 gap-y-1"
            data-testid="stale-publications-page"
          >
            <span>
              {t("statusPages:stalePublications.description", {
                count: group.publications.length,
                page: group.pageName,
              })}
            </span>
            {/*
              Straight to the entry when there is exactly one — that is the
              click that fixes it. With several, the page's own incident list
              is the honest destination.
            */}
            {group.publications.length === 1 ? (
              <Button asChild variant="outline" size="sm">
                <Link
                  to="/orgs/$org/status-pages/$statusPageUid/incidents/$uid"
                  params={{
                    org,
                    statusPageUid: group.pageUid,
                    uid: group.publications[0].uid,
                  }}
                  data-testid="stale-publication-link"
                >
                  {t("statusPages:stalePublications.review")}
                </Link>
              </Button>
            ) : (
              <Button asChild variant="outline" size="sm">
                <Link
                  to="/orgs/$org/status-pages/$statusPageUid"
                  params={{ org, statusPageUid: group.pageUid }}
                  data-testid="stale-publication-link"
                >
                  {t("statusPages:stalePublications.review")}
                </Link>
              </Button>
            )}
          </div>
        ))}
        <p className="text-sm opacity-90">
          {t("statusPages:stalePublications.hint")}
        </p>
      </AlertDescription>
    </Alert>
  );
}
