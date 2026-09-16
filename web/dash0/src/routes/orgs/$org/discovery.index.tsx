import { createFileRoute, redirect } from "@tanstack/react-router";

// Discovery moved under the Organization section (spec 2026-09-16-09), which
// also gives it the admin route guard it never had. This route keeps
// pasted-in-a-ticket / bookmarked URLs working.
export const Route = createFileRoute("/orgs/$org/discovery/")({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/orgs/$org/organization/discovery",
      params: { org: params.org },
    });
  },
});
