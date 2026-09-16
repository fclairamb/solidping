import { useQueries } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { CheckCircle2, Globe, Plus } from "lucide-react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { ApiError, apiFetch } from "@/api/client";
import type { Check, StatusPage } from "@/api/hooks";
import { useCreateResource, useStatusPages } from "@/api/hooks";
import {
  findCheckPublications,
  type CheckPublication,
} from "@/lib/check-publication";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";

/**
 * "Publish on a status page", from the check's side.
 *
 * This used to be a link straight to the CREATE-a-page form, which meant the
 * one button that sounds like the answer led an operator who already had a
 * page to making a second one (spec 2026-09-16-11). The dialog lists the pages
 * that exist, adds the check to a chosen section, and — the part that keeps it
 * honest — refuses to offer a duplicate row for a check that is already
 * visible, whether through its own row, through its GROUP's rolled-up
 * component, or through a section membership rule.
 */

/** One add button, bound to its own section's mutation hook. */
function AddToSectionButton({
  org,
  statusPageUid,
  sectionUid,
  checkUid,
  onAdded,
}: {
  org: string;
  statusPageUid: string;
  sectionUid: string;
  checkUid: string;
  onAdded: () => void;
}) {
  const { t } = useTranslation(["checks", "common"]);
  const createResource = useCreateResource(org, statusPageUid, sectionUid);

  return (
    <Button
      size="sm"
      variant="outline"
      disabled={createResource.isPending}
      data-testid={`publish-add-to-section-${sectionUid}`}
      onClick={async () => {
        try {
          await createResource.mutateAsync({ checkUid });
          onAdded();
        } catch (err) {
          toast.error(
            err instanceof ApiError
              ? err.message
              : t("checks:publish.addFailed"),
          );
        }
      }}
    >
      <Plus className="mr-1 h-3.5 w-3.5" />
      {t("checks:publish.add")}
    </Button>
  );
}

export function PublishOnStatusPageDialog({
  org,
  check,
  open,
  onOpenChange,
}: {
  org: string;
  check: Check;
  open: boolean;
  onOpenChange: (next: boolean) => void;
}) {
  const { t } = useTranslation(["checks", "common"]);

  // The list is cheap; the per-page detail is not fetched until the dialog is
  // actually opened, because `?with=sections` also runs the backstop
  // reconcile server-side.
  const { data: pageList, isLoading: listLoading } = useStatusPages(org, {
    enabled: open,
  });

  const detailQueries = useQueries({
    queries: (pageList ?? []).map((page) => ({
      queryKey: ["statusPage", org, page.uid, { with: "sections" }],
      queryFn: () =>
        apiFetch<StatusPage>(
          `/api/v1/orgs/${org}/status-pages/${page.uid}?with=sections`,
        ),
      enabled: open,
    })),
  });

  const detailsLoading = detailQueries.some((q) => q.isLoading);
  const pages = detailQueries
    .map((q) => q.data)
    .filter((p): p is StatusPage => p !== undefined);

  // Cheap enough to redo on every render (a handful of pages, a handful of
  // sections each) and memoizing it would mean memoizing `pages`, which
  // useQueries hands back as a fresh array each time anyway.
  const publications = findCheckPublications(check, pages);
  const publishedSections = new Set(publications.map((p) => p.sectionUid));

  const loading = listLoading || detailsLoading;
  const hasPages = (pageList ?? []).length > 0;

  const viaLine = (publication: CheckPublication) =>
    t(`checks:publish.via.${publication.via}`, {
      page: publication.pageName,
      section: publication.sectionName,
    });

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("checks:publish.title")}</DialogTitle>
          <DialogDescription>
            {t("checks:publish.description")}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          {loading && (
            <div className="space-y-2" data-testid="publish-dialog-loading">
              <Skeleton className="h-10 w-full" />
              <Skeleton className="h-10 w-full" />
            </div>
          )}

          {!loading && publications.length > 0 && (
            <Alert variant="success" data-testid="publish-already-published">
              <CheckCircle2 />
              <AlertTitle>{t("checks:publish.alreadyTitle")}</AlertTitle>
              <AlertDescription>
                <ul className="list-none space-y-1">
                  {publications.map((publication) => (
                    <li
                      key={`${publication.pageUid}-${publication.sectionUid}`}
                      data-testid={`publish-already-${publication.via}`}
                    >
                      {viaLine(publication)}
                    </li>
                  ))}
                </ul>
              </AlertDescription>
            </Alert>
          )}

          {!loading && !hasPages && (
            <p
              className="text-sm text-muted-foreground"
              data-testid="publish-no-status-pages"
            >
              {t("checks:publish.noStatusPages")}
            </p>
          )}

          {!loading &&
            pages.map((page) => (
              <div
                key={page.uid}
                className="rounded-lg border p-3"
                data-testid={`publish-page-${page.uid}`}
              >
                <div className="flex items-center gap-2 pb-2 text-sm font-medium">
                  <Globe className="h-4 w-4 text-muted-foreground" />
                  {page.name}
                </div>
                {(page.sections ?? []).length === 0 && (
                  <p className="text-xs text-muted-foreground">
                    {t("checks:publish.noSections")}
                  </p>
                )}
                <ul className="space-y-1">
                  {(page.sections ?? []).map((section) => {
                    const already = publishedSections.has(section.uid);
                    return (
                      <li
                        key={section.uid}
                        className="flex items-center justify-between gap-2 text-sm"
                      >
                        <span className="truncate">{section.name}</span>
                        {already ? (
                          <span
                            className="shrink-0 text-xs text-muted-foreground"
                            data-testid={`publish-section-published-${section.uid}`}
                          >
                            {t("checks:publish.sectionAlready")}
                          </span>
                        ) : (
                          <AddToSectionButton
                            org={org}
                            statusPageUid={page.uid}
                            sectionUid={section.uid}
                            checkUid={check.uid}
                            onAdded={() => {
                              toast.success(
                                t("checks:publish.added", {
                                  page: page.name,
                                  section: section.name,
                                }),
                              );
                            }}
                          />
                        )}
                      </li>
                    );
                  })}
                </ul>
              </div>
            ))}
        </div>

        <DialogFooter className="sm:justify-between">
          <Button asChild variant="outline">
            <Link
              to="/orgs/$org/status-pages/new"
              params={{ org }}
              search={{ checkUid: check.uid }}
              data-testid="publish-create-status-page"
            >
              <Plus className="mr-1 h-4 w-4" />
              {t("checks:publish.createNew")}
            </Link>
          </Button>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t("common:close")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
