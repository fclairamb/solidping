import { createFileRoute } from "@tanstack/react-router";
import { TvPage } from "@/components/tv/tv-route";

export const Route = createFileRoute("/$org/$slug/tv")({
  component: SlugTvRoute,
});

function SlugTvRoute() {
  const { org, slug } = Route.useParams();

  return <TvPage org={org} slug={slug} />;
}
