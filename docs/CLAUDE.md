# docs/ — Documentation Directory

Claude has full authority to organize this directory. It may rename, move, split, merge, or restructure any file or subdirectory here as it sees fit to keep the documentation clear, discoverable, and maintainable.

## Organization Rules

- **README.md** is the index — it must list every leaf document with a one-line description (subdirectory parents that are also indexes don't replace listing the children).
- Use subdirectories to group related documents (`conventions/`, `testing/`, etc.).
- Use lowercase kebab-case for all file and directory names.
- Keep filenames short and descriptive — drop redundant suffixes like `_conventions`.
- Competitive analysis lives in `competitors/`.
- JSON configs and manifests live near their related docs.
- When reorganizing, update all cross-references in CLAUDE.md, the root README, specs/, and any feature page that points to the old path.

## File-size limit (hard rule)

Markdown files in this tree must stay small enough that a contributor (or an LLM with a finite context window) can hold the whole document in their head. The numeric limits below are non-negotiable for new content; existing files that violate them must be reorganized before further additions.

- **Soft target: 500 lines.** Past this, prefer to split rather than keep growing the file.
- **Hard ceiling: 800 lines.** A file at or above this **must** be reorganized before any new content is added. No exceptions for "I'll just add one more section."
- **The cap is per file, not per topic.** If the topic genuinely needs more than 800 lines, split it across multiple focused files in a subdirectory — the topic does not get a waiver.

### How to split an oversized file

1. Create a sibling subdirectory with the same base name (e.g. `competitors/betterstack.md` → `competitors/betterstack/`).
2. Move each top-level section into its own child file, named after the section in kebab-case (`overview.md`, `monitoring.md`, `alerting.md`, `api.md`, `pricing.md`, `sources.md`, …). Aim for child files of 100–400 lines.
3. Replace the original file with a short `README.md` (or keep the parent file as an index) — under ~80 lines — that lists each child file with a one-line summary. The index is **navigation**, not content; do not duplicate prose between index and children.
4. Update `docs/README.md` to list every **leaf** child file, not just the parent directory. Discoverability lives at the leaf level.
5. Update every cross-reference in this repo (other docs, specs, server/CLAUDE.md, web/dash0/CLAUDE.md, source comments) so deep links keep resolving. Use `grep -rn old-path docs/ specs/ server/ web/` to find them.
6. After the split, run `wc -l` over the new tree and confirm every file is under the soft target.

### When to split *before* reaching the cap

- The file accumulates three or more clearly orthogonal subsections (e.g., "monitoring logic" vs. "alerting" vs. "API surface" vs. "pricing"). Splitting early is cheaper than splitting under pressure.
- A single section is being edited far more often than the rest — give it its own file so diffs are scoped.
- A reviewer asks "where is X?" and you find yourself scrolling — table of contents pain is a split signal.

### When *not* to split

- The file is under the soft target and has a single coherent topic — leave it alone. Splitting prematurely creates navigation noise.
- The "split" would just be the same content in two files because the sections aren't actually orthogonal — fix the structure first, then split.

## Current Structure

```
docs/
  README.md                    # Full index (every leaf file)
  CLAUDE.md                    # This file
  architecture.md              # System architecture overview
  api-specification.md         # REST API specification
  database-model.md            # Database schema & tables
  roadmap.md                   # Current priorities snapshot
  conventions/                 # Project conventions
  features/                    # End-to-end feature pages
  testing/                     # Test infrastructure & fixtures
  slack/                       # Slack app manifests
  research/                    # Tool evaluations & comparisons
  competitors/                 # Competitor analysis
    {name}.md or {name}/       # One file per competitor; subdir if oversized
```
