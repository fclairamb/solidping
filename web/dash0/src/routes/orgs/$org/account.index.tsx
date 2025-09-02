import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/orgs/$org/account/")({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/orgs/$org/account/profile",
      params: { org: params.org },
    });
  },
});
