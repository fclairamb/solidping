import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/organization/")({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/orgs/$org/organization/members",
      params: { org: params.org },
    });
  },
});
