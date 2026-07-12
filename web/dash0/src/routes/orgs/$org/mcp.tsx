import { createFileRoute, redirect } from "@tanstack/react-router";

type McpRedirectSearch = {
  /** "get" when the user landed here from opening GET /api/v1/mcp in a browser. */
  from?: string;
};

// The "AI assistants" / MCP setup page moved under the Account section
// (/orgs/$org/account/mcp) — connecting an MCP client is user-level setup,
// which is exactly what the Account section holds. Keep this route as a
// redirect so existing bookmarks and external links don't 404, and preserve
// the ?from=get contextual hint through the redirect.
export const Route = createFileRoute("/orgs/$org/mcp")({
  validateSearch: (search: Record<string, unknown>): McpRedirectSearch => ({
    from: typeof search.from === "string" ? search.from : undefined,
  }),
  beforeLoad: ({ params, search }) => {
    throw redirect({
      to: "/orgs/$org/account/mcp",
      params: { org: params.org },
      search,
      replace: true,
    });
  },
});
