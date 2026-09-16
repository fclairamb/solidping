import { createFileRoute, redirect } from "@tanstack/react-router";

// Discovery moved under the Organization section (spec 2026-09-16-09). This
// route keeps pasted-in-a-ticket / bookmarked scan-detail URLs working.
export const Route = createFileRoute("/orgs/$org/discovery/$jobUid/")({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/orgs/$org/organization/discovery/$jobUid",
      params: { org: params.org, jobUid: params.jobUid },
    });
  },
});
