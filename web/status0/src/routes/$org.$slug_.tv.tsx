import { createFileRoute } from "@tanstack/react-router";
import { TvPage } from "@/components/tv/tv-route";

/**
 * TV mode for a named status page (spec 2026-08-29-08).
 *
 * The file is `$org.$slug_.tv.tsx`, not `$org.$slug.tv.tsx`, and the trailing
 * underscore is load-bearing: `$org.$slug.tsx` is a leaf route that renders the
 * ordinary status page and has no <Outlet/>, so nesting under it silently
 * renders the SCROLLING page at the /tv URL instead of the board. The
 * underscore opts this route out of that parent while keeping the same path.
 */

export const Route = createFileRoute("/$org/$slug_/tv")({
  component: SlugTvRoute,
});

function SlugTvRoute() {
  const { org, slug } = Route.useParams();

  return <TvPage org={org} slug={slug} />;
}
