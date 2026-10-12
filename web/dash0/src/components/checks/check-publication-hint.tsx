import { useQuery } from "@tanstack/react-query";
import { Globe, Info } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { Check } from "@/api/hooks";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { fetchCheckPublications } from "@/lib/check-publication";

/**
 * Inline "where is my new check visible" line, shown on the detail page right
 * after a create (spec 2026-10-12-05). It replaces a late toast that landed on
 * top of the header actions. It renders in the page body, never over the
 * header, and stays until the user leaves the page.
 *
 * A failed lookup renders nothing: the check IS created, a status-page lookup
 * that errors must not look like a failure.
 */
export function CheckPublicationHint({
  org,
  check,
  onPublish,
}: {
  org: string;
  check: Pick<Check, "uid" | "checkGroupUid" | "labels">;
  onPublish: () => void;
}) {
  const { t } = useTranslation("checks");
  const { data } = useQuery({
    queryKey: ["check-publication-hint", org, check.uid],
    queryFn: () => fetchCheckPublications(org, check),
    retry: false,
  });

  if (!data) return null;
  const first = data[0];

  return (
    <Alert
      variant={first ? "success" : "default"}
      data-testid="check-publication-hint"
    >
      {first ? <Globe /> : <Info />}
      <AlertDescription className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <span className="min-w-0 break-words">
          {first
            ? t("publish.postCreateOn", {
                page: first.pageName,
                section: first.sectionName,
              })
            : t("publish.postCreateNone")}
        </span>
        {!first && (
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={onPublish}
            data-testid="check-publication-hint-action"
          >
            {t("publish.postCreateAction")}
          </Button>
        )}
      </AlertDescription>
    </Alert>
  );
}
