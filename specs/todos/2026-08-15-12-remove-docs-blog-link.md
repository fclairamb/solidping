---
model: sonnet
effort: low
---

# The docs site links to a Blog that should no longer be advertised

## Problem

The Docusaurus docs site advertises a blog in two places, both pointing at
`https://www.solidping.io/blog`:

1. **Navbar** — a `Blog` item between `Home` and `GitHub`
   ([docusaurus.config.ts:130-134](web/docs/docusaurus.config.ts:130)).
2. **Footer** — a `Blog` entry in the `SolidPing` column, between `Home` and
   `Terms of Service`
   ([docusaurus.config.ts:182-185](web/docs/docusaurus.config.ts:184)).

Both are external links into the marketing site, which lives in the separate
`solidping-website` repo — the docs site itself has the Docusaurus blog plugin
disabled (`blog: false`,
[docusaurus.config.ts:55](web/docs/docusaurus.config.ts:55), and
`indexBlog: false` at [:103](web/docs/docusaurus.config.ts:103)), so nothing
here generates blog content.

These are the only two `/blog` references in the shipped frontends — `dash0`,
`status0` and the docs content pages have none.

## Proposal

Remove both link entries from `web/docs/docusaurus.config.ts`:

- Delete the navbar `Blog` item, leaving `Home` (left) and `GitHub` (right).
- Delete the footer `Blog` item, leaving the `SolidPing` column as `Home` and
  `Terms of Service`.

Leave everything else alone:

- Keep `blog: false` and `indexBlog: false` — they are already correct and
  unrelated to the navigation links.
- Do not touch the `solidping-website` repo; this spec is scoped to the
  embedded docs site only.

Verification: `make build-docs` must pass, and neither `Blog` nor
`solidping.io/blog` may remain anywhere under `web/docs/` afterwards
(`grep -rn "/blog" web/docs/` should return only the two disabled-plugin
comments/flags, if anything).

## Resolved open questions

The instruction was simply "remove the blog link". Scope decisions taken, so
there is nothing left to decide at implementation time:

**Decision:** Remove the links only — do **not** attempt to determine whether
`www.solidping.io/blog` still exists, and do **not** make any change in the
`solidping-website` repo. If the marketing blog is later revived, restoring
two config entries is trivial.

**Decision:** Both occurrences (navbar and footer) are in scope. "The blog
link" is singular in the request but there are two, and leaving one behind
would be the obviously wrong reading.
