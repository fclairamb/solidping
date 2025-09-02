import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/server/")({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/orgs/$org/server/web",
      params: { org: params.org },
    });
  },
});
