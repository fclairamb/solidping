import { useEffect } from "react";
import { createFileRoute, redirect, useNavigate } from "@tanstack/react-router";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useCheck } from "@/api/hooks";
import {
  validateBadgeSearch,
  type BadgeSearch,
} from "./checks.$checkUid.badges";

// The badge builder moved under the check it belongs to
// (/orgs/$org/checks/$checkUid/badges, spec 2026-09-16-08). This route keeps
// the pasted-in-a-ticket URLs working and renders NO builder of its own —
// there is exactly one implementation, and it lives on the child route.
interface LegacyBadgeSearch extends BadgeSearch {
  check?: string;
}

export const Route = createFileRoute("/orgs/$org/badges")({
  validateSearch: (search: Record<string, unknown>): LegacyBadgeSearch => ({
    ...validateBadgeSearch(search),
    check: typeof search.check === "string" && search.check ? search.check : undefined,
  }),
  beforeLoad: ({ params, search }) => {
    // No check named: there is nothing to build a badge for, so send the user
    // to the list that lets them pick one.
    if (!search.check) {
      throw redirect({ to: "/orgs/$org/checks", params: { org: params.org } });
    }
  },
  component: LegacyBadgesRedirect,
});

function LegacyBadgesRedirect() {
  const { org } = Route.useParams();
  const { check: identifier, components, period, style, label, minWidth, width } =
    Route.useSearch();
  const navigate = useNavigate();

  // `?check=` accepts a slug OR a uid, and the new route is keyed on the uid,
  // so resolve it the same way the old page did — a direct fetch, which also
  // covers a check outside the check list's first page.
  const { data: check, isLoading } = useCheck(org, identifier ?? "");

  useEffect(() => {
    if (!identifier || isLoading) return;
    navigate({
      to: "/orgs/$org/checks/$checkUid/badges",
      // An identifier that resolves to nothing is forwarded as-is: the child
      // route owns the "no such check" state, so there is one 404, not two.
      params: { org, checkUid: check?.uid ?? identifier },
      search: { components, period, style, label, minWidth, width },
      replace: true,
    });
  }, [
    check,
    isLoading,
    identifier,
    navigate,
    org,
    components,
    period,
    style,
    label,
    minWidth,
    width,
  ]);

  return (
    <Card>
      <CardContent className="py-8">
        <Skeleton className="h-48 w-full" data-testid="badge-redirect-loading" />
      </CardContent>
    </Card>
  );
}
