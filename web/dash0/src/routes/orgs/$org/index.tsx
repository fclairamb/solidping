import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/")({
  beforeLoad: ({ params, location }) => {
    // Don't redirect when an OAuth callback drops tokens here; let OrgLayout
    // consume them. Otherwise this redirect strips ?access_token=... and the
    // login flow ends up on the wrong org's login page.
    if (new URLSearchParams(location.searchStr || "").has("access_token")) {
      return;
    }
    throw redirect({
      to: "/orgs/$org/checks",
      params: { org: params.org },
    });
  },
});
