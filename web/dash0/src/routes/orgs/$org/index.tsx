import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/")({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/orgs/$org/checks",
      params: { org: params.org },
    });
  },
});
