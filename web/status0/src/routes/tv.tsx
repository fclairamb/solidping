import { createFileRoute } from "@tanstack/react-router";
import { TvPage, TvNotConfigured } from "@/components/tv/tv-route";
import { readSpPage } from "@/lib/sp-page";

/**
 * TV mode at the root of a CUSTOM DOMAIN (spec 2026-08-29-08).
 *
 * On a custom host the server resolves the page from the request's Host and
 * stamps `<meta name="sp-page" content="org/slug">` into the SPA shell — the
 * same bootstrap the "/" route uses. So `https://status.acme.com/tv` needs no
 * org or slug in the path.
 *
 * On the installation's own host there is no such tag, and this route
 * explains itself rather than rendering a blank screen.
 */
export const Route = createFileRoute("/tv")({
  component: CustomDomainTvRoute,
});

function CustomDomainTvRoute() {
  const spPage = readSpPage();

  if (!spPage) return <TvNotConfigured />;

  return <TvPage org={spPage.org} slug={spPage.slug} />;
}
