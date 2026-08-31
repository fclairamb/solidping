import { createFileRoute } from "@tanstack/react-router";
import { TvPage } from "@/components/tv/tv-route";

/**
 * TV mode for an organization's DEFAULT status page (spec 2026-08-29-08).
 *
 * Static `tv` outranks the `$slug` parameter in TanStack Router, so an org
 * that also has a page whose slug is literally "tv" loses `/{org}/tv` to this
 * route. Accepted and documented (web/docs/docs/features/status-page-tv-mode.md):
 * "point the TV at the org" is the shape an operator reaches for first, and a
 * page slugged `tv` is still reachable at `/{org}/tv/tv`.
 */
export const Route = createFileRoute("/$org/tv")({
  component: DefaultTvRoute,
});

function DefaultTvRoute() {
  const { org } = Route.useParams();

  return <TvPage org={org} />;
}
