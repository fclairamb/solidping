import { createFileRoute, redirect } from "@tanstack/react-router";

// The "AI assistants" page moved from the Organization settings section to
// the Account section (/orgs/$org/account/mcp) — connecting an MCP client is
// user-level setup, not org configuration. Keep this route as a redirect so
// existing bookmarks and external links don't 404. Redirect straight to the
// current path to avoid a redirect chain.
export const Route = createFileRoute("/orgs/$org/organization/ai")({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/orgs/$org/account/mcp",
      params: { org: params.org },
      replace: true,
    });
  },
});
