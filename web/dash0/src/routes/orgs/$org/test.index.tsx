import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/test/")({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/orgs/$org/test/templates",
      params: { org: params.org },
    });
  },
});
