import { createFileRoute, redirect } from "@tanstack/react-router";

// Discovery moved under the Organization section (spec 2026-09-16-09). This
// route keeps pasted-in-a-ticket / bookmarked URLs working, including a
// deep link to a specific scan method (?method=kubernetes, etc.).
export const Route = createFileRoute("/orgs/$org/discovery/new")({
  validateSearch: (search: Record<string, unknown>): { method?: string } => {
    const m = search.method;
    return typeof m === "string" && m.length > 0 ? { method: m } : {};
  },
  beforeLoad: ({ params, search }) => {
    throw redirect({
      to: "/orgs/$org/organization/discovery/new",
      params: { org: params.org },
      search,
    });
  },
});
