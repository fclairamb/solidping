---
model: sonnet
effort: medium
---

# Incident page comments render as plain text and blend into the Comments card

## Problem

Comments on the incident detail page are rendered too simply, in two ways:

1. **No text formatting.** The comment body is rendered as raw text in
   `web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx:707-709`:

   ```tsx
   <p className="whitespace-pre-wrap break-words text-sm">
     {getCommentText(c)}
   </p>
   ```

   Concretely missing:
   - **URL linkification** — a pasted `https://www.example.com/...` should
     become a clickable link.
   - **Inline code** — `` `symbol` `` should render as inline code
     (monospace, subtle background).
   - **Code blocks** — triple-backtick ``` fenced blocks ``` should render
     as a preformatted block.

   Comments arrive from several sources (dash0 form, Slack, Telegram, API),
   so URLs and code snippets are common — error messages, endpoints, curl
   commands — and today they all render as flat prose.

2. **No visual separation.** Each comment `<li>` uses
   `rounded-md border bg-muted/30 p-3`
   (`incidents.$incidentUid.tsx:680`), which is nearly indistinguishable
   from the surrounding "Comments" `Card` background — the individual
   comment bubbles don't read as distinct items inside the card.

## Proposal

1. **Add a small comment-body renderer** (e.g.
   `web/dash0/src/components/shared/comment-body.tsx`) used by the
   `CommentsCard` in place of the plain `<p>`. Scope it to exactly:
   - autolinked URLs (`http://` / `https://`) — `target="_blank"`,
     `rel="noopener noreferrer"`, styled as a normal link;
   - `` `inline code` ``;
   - fenced ``` code blocks ``` (own `<pre>` block, `overflow-x-auto` so
     long lines scroll instead of breaking the mobile layout).

   Prefer a tiny hand-rolled tokenizer over pulling `react-markdown` into
   dash0: comments are short, sources are untrusted (Slack/Telegram/API),
   and full markdown (headings, images, HTML) is explicitly *not* wanted
   here. All output goes through React elements — never
   `dangerouslySetInnerHTML`. Preserve the existing `whitespace-pre-wrap
   break-words` behavior for plain-text segments. If a shared renderer with
   status0's `react-markdown` usage seems tempting, note status0 renders
   trusted operator-authored markdown while comments are freeform chat —
   the two have different threat models and feature sets; keep them apart.

2. **Distinct comment background.** Give each comment bubble a background
   that visibly separates it from the card body — e.g. `bg-background` (or
   `bg-card`-contrasting token) with the existing border, or a stronger
   muted tone — verified in both light and dark mode against the design
   reference (`web/dash0/src/routes/orgs/$org/design-reference.tsx`).
   Whatever is chosen, add the comment-bubble pattern to the design
   reference page so the catalog stays canonical.

3. **Tests.**
   - Unit tests for the tokenizer/renderer: plain text passes through,
     URLs become anchors (and don't swallow trailing punctuation),
     backticks inside code blocks are not double-parsed, unclosed fences
     degrade gracefully, no HTML injection (`<script>` stays literal text).
   - Extend the existing incident-comments E2E to post a comment
     containing a URL, an inline code span, and a fenced block, and assert
     the anchor/`<code>`/`<pre>` elements render.

## Open questions

- Should the same renderer apply to comment text shown elsewhere (e.g. the
  ack/comment notices in emails or the event feed)? Out of scope here —
  this spec covers the incident page's Comments card only.
