---
version: "0.1.2"
level: auto
processes:
  design: copilot
  implementation: auto
  testing: auto
  documentation: auto
  review: assist
  deployment: copilot
---

This format is based on [AI-DECLARATION.md](https://ai-declaration.md/en/0.1.2).

## Notes

SolidPing is written mostly by AI agents, on purpose, under a spec-driven
workflow. This file says how, so that anyone who wants to audit the generated
parts knows where to look.

**How the work flows.** A change starts as a spec in [`specs/`](specs/) — a
`## Problem` grounded in observed behaviour, a `## Proposal`, and an
`## Out of scope`. The spec carries `model:` and `effort:` frontmatter, and is
dispatched to an agent that implements it end to end. 783 specs have been
completed this way. Every change lands through a pull request: CI must be green
and a human merges. Nothing reaches `main` unreviewed, and nothing reaches
`main` unbuilt.

**What that measures out to**, on `main` as of 2026-09-05:

| | |
|---|---|
| Commits on `main` | 299 |
| …from Renovate (dependency bumps) | 148 |
| …human-authored, carrying an AI co-author trailer | 107 of the remaining 151 (71%) |
| Completed specs | 783 |
| Go, excluding tests | ~340k lines |
| Go tests | ~257k lines |

Commits are squash-merged, so the `Co-Authored-By:` trailers in a merge commit
name every model that worked on the branch. `git log` is the authoritative
record; the table above is derived from it, not from memory.

**Per-process detail**, beyond what the frontmatter can express:

- `design: copilot` — architecture is argued in the spec before code exists.
  The AI drafts the proposal and the trade-offs; a human sets the direction and
  approves it by merging. Business decisions that constrain the design (pricing
  tiers, plan limits) are human-made and referenced from the code that
  implements them.
- `implementation: auto` and `testing: auto` — an agent takes a spec to
  completion, tests included. Tests are written with the change, not after it,
  which is why the test tree is nearly as large as the code it covers.
- `documentation: auto` — this file, the README, [`web/docs`](web/docs) and the
  engineering [`wiki/`](wiki) are all AI-written under the same flow.
- `review: assist` — a human is the reviewer of record and holds the merge
  button. [`.github/workflows/claude.yml`](.github/workflows/claude.yml)
  invokes an agent only when someone writes `@claude` on an issue or a review
  comment, so AI review is on demand, not automatic.
- `deployment: copilot` — CI, release automation and deployment config are
  AI-written and human-approved; 29 of the 48 commits touching
  [`.github/`](.github) carry an AI co-author.

**What this does not claim.** The declaration describes how the code was
produced, not how well. Bugs here are ours regardless of who typed them, and
`level: auto` is not an excuse offered in advance — please report anything that
looks wrong.
