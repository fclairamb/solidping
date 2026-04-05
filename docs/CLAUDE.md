# docs/ — Documentation Directory

Claude has full authority to organize this directory. It may rename, move, split, merge, or restructure any file or subdirectory here as it sees fit to keep the documentation clear, discoverable, and maintainable.

## Organization Rules

- **README.md** is the index — it must list every document with a one-line description
- Use subdirectories to group related documents (conventions/, testing/, etc.)
- Use lowercase kebab-case for all file and directory names
- Keep filenames short and descriptive — drop redundant suffixes like `_conventions`
- Competitive analysis lives in `competitors/`
- JSON configs and manifests live near their related docs
- When reorganizing, update all cross-references in CLAUDE.md and specs/

## Current Structure

```
docs/
  README.md                    # Full index
  CLAUDE.md                    # This file
  architecture.md              # System architecture overview
  api-specification.md         # REST API specification
  database-model.md            # Database schema & tables
  conventions/                 # All project conventions
  testing/                     # Test infrastructure & fixtures
  slack/                       # Slack app manifests
  research/                    # Tool evaluations & comparisons
  competitors/                 # Competitor analysis
```
