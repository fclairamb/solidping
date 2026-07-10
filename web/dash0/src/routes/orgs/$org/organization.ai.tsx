import { createFileRoute, redirect } from "@tanstack/react-router";

// The "AI assistants" page moved from the Organization settings section to
// the org-level /orgs/$org/mcp route (connecting an MCP client is user-level
// setup, not org configuration). Keep this route as a redirect so existing
// bookmarks and external links don't 404.
export const Route = createFileRoute("/orgs/$org/organization/ai")({
  beforeLoad: ({ params }) => {
    throw redirect({
      to: "/orgs/$org/mcp",
      params: { org: params.org },
      replace: true,
    });
  },
});
